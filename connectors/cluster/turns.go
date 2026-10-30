// Package cluster spreads an agent's turns over every instance running it,
// through NATS JetStream: a work queue of turns, their events relayed to the
// instance holding the caller, and conversations kept where any instance
// reads them.
package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/lalternative/packages/go/cortex/host"
)

const (
	kindHeader  = "Cortex-Kind"
	kindEvent   = "event"
	kindStarted = "started"
	kindBeat    = "beat"
	kindEnd     = "end"
	errorHeader = "Cortex-Error"

	beatEvery = 10 * time.Second
	ackWait   = time.Minute
	stateTTL  = 15 * time.Minute
	lockTTL   = 6 * beatEvery
	busyRetry = 500 * time.Millisecond
	opTimeout = 5 * time.Second

	DefaultDrain = 90 * time.Second
)

// Config names what Turns needs beyond the connection.
type Config struct {
	// Agent scopes the stream, subjects and buckets: instances of one agent
	// share them, other agents never see them.
	Agent string
	// Concurrency is how many turns this instance runs at once.
	Concurrency int
	// QueueWait is how long a turn may wait for an instance to take it.
	QueueWait time.Duration
	// Silence is how long a started turn may go without a word from the
	// instance running it before it is taken for lost.
	Silence time.Duration
	// Drain is how long an instance told to stop keeps running the turns it
	// took, taking no new one, before it cuts them.
	Drain time.Duration
}

// Turns is host.Turns over NATS.
type Turns struct {
	nc     *nats.Conn
	js     jetstream.JetStream
	cfg    Config
	name   string
	stream jetstream.Stream
	state  jetstream.KeyValue
	locks  jetstream.KeyValue
}

var _ host.Turns = (*Turns)(nil)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

var safeTaskID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func token(agent string) string {
	return strings.ToLower(unsafeName.ReplaceAllString(agent, "_"))
}

func NewTurns(ctx context.Context, nc *nats.Conn, cfg Config) (*Turns, error) {
	if strings.TrimSpace(cfg.Agent) == "" {
		return nil, errors.New("cluster: the agent needs a name")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = host.DefaultConcurrency
	}
	if cfg.QueueWait <= 0 {
		cfg.QueueWait = host.DefaultQueueWait
	}
	if cfg.Silence <= 0 {
		cfg.Silence = 6 * beatEvery
	}
	if cfg.Drain <= 0 {
		cfg.Drain = DefaultDrain
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	name := token(cfg.Agent)
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	stream, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "CORTEX_" + strings.ToUpper(name) + "_TURNS",
		Subjects:  []string{"cortex." + name + ".turns"},
		Retention: jetstream.WorkQueuePolicy,
		Storage:   jetstream.MemoryStorage,
		MaxAge:    stateTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: turns stream: %w", err)
	}
	state, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  "cortex_" + name + "_turns",
		TTL:     stateTTL,
		Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: turn state: %w", err)
	}
	locks, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  "cortex_" + name + "_locks",
		TTL:     lockTTL,
		Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: conversation locks: %w", err)
	}
	return &Turns{nc: nc, js: js, cfg: cfg, name: name, stream: stream, state: state, locks: locks}, nil
}

func (t *Turns) subject(kind string, task a2a.TaskID) string {
	return "cortex." + t.name + "." + kind + "." + string(task)
}

// Dispatch queues the turn and relays its events until its final one. The
// subscription buffers without bound: a caller reading slowly must not lose
// the end of its answer.
func (t *Turns) Dispatch(ctx context.Context, turn host.Turn, emit host.Emit) error {
	if !safeTaskID.MatchString(string(turn.TaskID)) {
		return fmt.Errorf("cluster: task id %q cannot name a subject", turn.TaskID)
	}
	sub, err := t.nc.SubscribeSync(t.subject("events", turn.TaskID))
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := sub.SetPendingLimits(-1, -1); err != nil {
		return err
	}
	if err := t.nc.Flush(); err != nil {
		return err
	}
	job, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	if _, err := t.js.Publish(ctx, "cortex."+t.name+".turns", job); err != nil {
		return fmt.Errorf("cluster: turn not queued: %w", err)
	}

	started := false
	deadline := time.Now().Add(t.cfg.QueueWait)
	for {
		waitCtx, cancel := context.WithDeadline(ctx, deadline)
		msg, err := sub.NextMsgWithContext(waitCtx)
		cancel()
		if err != nil {
			t.cancel(turn.TaskID)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("cluster: turn events: %w", err)
			}
			if started {
				return errors.New("the instance running this turn went silent")
			}
			return host.ErrBusy
		}
		switch msg.Header.Get(kindHeader) {
		case kindStarted:
			started = true
		case kindEnd:
			if why := msg.Header.Get(errorHeader); why != "" {
				return errors.New(why)
			}
			return nil
		case kindEvent:
			ev, err := a2a.UnmarshalEventJSON(msg.Data)
			if err != nil {
				t.cancel(turn.TaskID)
				return fmt.Errorf("cluster: unreadable event: %w", err)
			}
			if err := emit(ctx, ev); err != nil {
				t.cancel(turn.TaskID)
				return err
			}
		}
		if started {
			deadline = time.Now().Add(t.cfg.Silence)
		}
	}
}

func (t *Turns) cancel(task a2a.TaskID) {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if _, err := t.state.Put(ctx, "cancel."+string(task), []byte{1}); err != nil {
		slog.Warn("cluster: cancel not recorded", "task_id", task, "error", err)
	}
	_ = t.nc.Publish(t.subject("cancel", task), nil)
}

// Serve takes turns from the queue, Concurrency at once, until ctx ends; the
// turns it took then run to their end, for Drain at most.
func (t *Turns) Serve(ctx context.Context, run host.Runner) error {
	runs, cutRuns := context.WithCancel(context.WithoutCancel(ctx))
	defer cutRuns()
	go func() {
		<-ctx.Done()
		select {
		case <-time.After(t.cfg.Drain):
			cutRuns()
		case <-runs.Done():
		}
	}()
	cons, err := t.stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "workers",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxAckPending: -1,
	})
	if err != nil {
		return fmt.Errorf("cluster: turns consumer: %w", err)
	}
	var wg sync.WaitGroup
	for range t.cfg.Concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
				if err != nil {
					if !errors.Is(err, nats.ErrTimeout) && !errors.Is(err, jetstream.ErrNoMessages) && ctx.Err() == nil {
						slog.Warn("cluster: next turn", "error", err)
						time.Sleep(time.Second)
					}
					continue
				}
				t.take(runs, msg, run)
			}
		}()
	}
	wg.Wait()
	return nil
}

func (t *Turns) take(ctx context.Context, msg jetstream.Msg, run host.Runner) {
	var turn host.Turn
	if err := json.Unmarshal(msg.Data(), &turn); err != nil || !safeTaskID.MatchString(string(turn.TaskID)) {
		slog.Error("cluster: unreadable turn dropped", "error", err)
		_ = msg.Term()
		return
	}
	if t.flagged(ctx, "cancel."+string(turn.TaskID)) {
		_ = msg.Ack()
		return
	}
	if t.flagged(ctx, "started."+string(turn.TaskID)) {
		t.end(turn.TaskID, errors.New("the instance running this turn stopped before its end"))
		_ = msg.Ack()
		return
	}
	lock := hashed(turn.Conversation())
	if _, err := t.locks.Create(ctx, lock, []byte(turn.TaskID)); err != nil {
		_ = msg.NakWithDelay(busyRetry)
		return
	}
	defer func() { _ = t.locks.Purge(context.Background(), lock) }()
	_, _ = t.state.Put(ctx, "started."+string(turn.TaskID), []byte{1})
	_ = msg.Ack()

	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	cancelSub, err := t.nc.Subscribe(t.subject("cancel", turn.TaskID), func(*nats.Msg) { stop() })
	if err == nil {
		defer func() { _ = cancelSub.Unsubscribe() }()
	}
	t.signal(turn.TaskID, kindStarted)
	go t.keepAlive(runCtx, turn.TaskID, lock)

	emit := func(_ context.Context, ev a2a.Event) error {
		data, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		out := nats.NewMsg(t.subject("events", turn.TaskID))
		out.Header.Set(kindHeader, kindEvent)
		out.Data = data
		return t.nc.PublishMsg(out)
	}
	t.end(turn.TaskID, run(runCtx, turn, emit))
}

func (t *Turns) keepAlive(ctx context.Context, task a2a.TaskID, lock string) {
	beat := time.NewTicker(beatEvery)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-beat.C:
			t.signal(task, kindBeat)
			_, _ = t.locks.Put(ctx, lock, []byte(task))
		}
	}
}

func (t *Turns) signal(task a2a.TaskID, kind string) {
	out := nats.NewMsg(t.subject("events", task))
	out.Header.Set(kindHeader, kind)
	_ = t.nc.PublishMsg(out)
}

func (t *Turns) end(task a2a.TaskID, err error) {
	out := nats.NewMsg(t.subject("events", task))
	out.Header.Set(kindHeader, kindEnd)
	if err != nil {
		out.Header.Set(errorHeader, err.Error())
	}
	_ = t.nc.PublishMsg(out)
	_ = t.nc.Flush()
}

func (t *Turns) flagged(ctx context.Context, key string) bool {
	_, err := t.state.Get(ctx, key)
	return err == nil
}

func hashed(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
