package host

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
)

// Turn is one A2A message to answer: everything a host needs to run it, on
// whichever instance takes it.
type Turn struct {
	TaskID    a2a.TaskID   `json:"task_id"`
	ContextID string       `json:"context_id"`
	Message   *a2a.Message `json:"message"`
	Resumed   bool         `json:"resumed,omitempty"`
}

func (t Turn) TaskInfo() a2a.TaskInfo {
	return a2a.TaskInfo{TaskID: t.TaskID, ContextID: t.ContextID}
}

// Conversation is the key turns of one conversation share: they run one at a
// time, in the order they were taken.
func (t Turn) Conversation() string {
	subject, _, _ := metadata(t.Message)
	return conversationKey(subject, t.ContextID)
}

// Emit passes one event of a running turn to the caller waiting on it.
type Emit func(ctx context.Context, event a2a.Event) error

// Runner runs a turn to its end, emitting its final event last.
type Runner func(ctx context.Context, t Turn, emit Emit) error

// Turns decides where and when a turn runs. Dispatch is called by the
// instance holding the caller's connection; Serve by every instance able to
// run turns. A single instance does both.
type Turns interface {
	Dispatch(ctx context.Context, t Turn, emit Emit) error
	Serve(ctx context.Context, run Runner) error
}

// ErrBusy is returned when no instance took the turn within the wait allowed.
var ErrBusy = errors.New("the agent is busy: no room for this turn, try again shortly")

const (
	DefaultConcurrency = 8
	DefaultQueueWait   = 2 * time.Minute
)

// LocalTurns runs turns in this process: at most Concurrency at once, the
// turns of one conversation one after the other, and a turn that waits longer
// than QueueWait for room is refused with ErrBusy.
type LocalTurns struct {
	slots chan struct{}
	wait  time.Duration
	locks *conversationLocks

	mu  sync.Mutex
	run Runner
}

func NewLocalTurns(concurrency int, queueWait time.Duration) *LocalTurns {
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	if queueWait <= 0 {
		queueWait = DefaultQueueWait
	}
	return &LocalTurns{slots: make(chan struct{}, concurrency), wait: queueWait, locks: newConversationLocks()}
}

func (l *LocalTurns) Serve(ctx context.Context, run Runner) error {
	l.mu.Lock()
	l.run = run
	l.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (l *LocalTurns) bind(run Runner) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run == nil {
		l.run = run
	}
}

func (l *LocalTurns) Dispatch(ctx context.Context, t Turn, emit Emit) error {
	l.mu.Lock()
	run := l.run
	l.mu.Unlock()
	if run == nil {
		return errors.New("host: no runner bound to the local turns")
	}
	waitCtx, cancel := context.WithTimeout(ctx, l.wait)
	defer cancel()

	unlock, err := l.locks.lock(waitCtx, t.Conversation())
	if err != nil {
		return waitError(ctx, err)
	}
	defer unlock()

	select {
	case l.slots <- struct{}{}:
	case <-waitCtx.Done():
		return waitError(ctx, waitCtx.Err())
	}
	defer func() { <-l.slots }()
	return run(ctx, t, emit)
}

func waitError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrBusy
	}
	return err
}

type conversationLocks struct {
	mu    sync.Mutex
	held  map[string]chan struct{}
	users map[string]int
}

func newConversationLocks() *conversationLocks {
	return &conversationLocks{held: map[string]chan struct{}{}, users: map[string]int{}}
}

func (c *conversationLocks) lock(ctx context.Context, key string) (func(), error) {
	c.mu.Lock()
	ch, ok := c.held[key]
	if !ok {
		ch = make(chan struct{}, 1)
		c.held[key] = ch
	}
	c.users[key]++
	c.mu.Unlock()

	release := func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.users[key]--
		if c.users[key] == 0 {
			delete(c.users, key)
			delete(c.held, key)
		}
	}
	select {
	case ch <- struct{}{}:
		return func() { <-ch; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
