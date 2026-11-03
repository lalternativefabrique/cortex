package schedule

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// Run asks the agent a task's prompt in the task's conversation and returns
// its answer. The runner never runs one task twice at once.
type Run func(ctx context.Context, t Task) (string, error)

// Deliver hands a finished run to whoever should see it: a log, a channel.
type Deliver func(ctx context.Context, t Task, answer string, err error)

// DefaultReload is how often the runner rereads the store, so a task
// written by hand or by the agent is picked up without a restart.
const DefaultReload = time.Minute

// Runner keeps the store's tasks on their clocks.
type Runner struct {
	Store   Store
	Run     Run
	Deliver Deliver
	// Location is the clock tasks are read in. Nil means the machine's.
	Location *time.Location
	// Reload is how often the store is reread. Zero means DefaultReload.
	Reload time.Duration
	// Timeout bounds one run. Zero means no bound beyond the context's.
	Timeout time.Duration

	mu      sync.Mutex
	cron    *cron.Cron
	entries map[string]cron.EntryID
	tasks   map[string]Task
	running map[string]bool
}

// Serve runs the tasks until ctx ends.
func (r *Runner) Serve(ctx context.Context) error {
	loc := r.Location
	if loc == nil {
		loc = time.Local
	}
	reload := r.Reload
	if reload <= 0 {
		reload = DefaultReload
	}
	r.mu.Lock()
	r.cron = cron.New(cron.WithParser(parser), cron.WithLocation(loc))
	r.entries = map[string]cron.EntryID{}
	r.tasks = map[string]Task{}
	r.running = map[string]bool{}
	r.mu.Unlock()
	r.cron.Start()
	defer func() { <-r.cron.Stop().Done() }()

	ticker := time.NewTicker(reload)
	defer ticker.Stop()
	for {
		if err := r.Refresh(ctx); err != nil {
			slog.Warn("schedule: tasks not reloaded", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Refresh rereads the store and schedules what changed.
func (r *Runner) Refresh(ctx context.Context) error {
	tasks, err := r.Store.List(ctx)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cron == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, t := range tasks {
		seen[t.Name] = true
		if known, ok := r.tasks[t.Name]; ok && known == t {
			continue
		}
		if id, ok := r.entries[t.Name]; ok {
			r.cron.Remove(id)
			delete(r.entries, t.Name)
		}
		r.tasks[t.Name] = t
		if t.Paused {
			continue
		}
		if err := t.Valid(); err != nil {
			slog.Warn("schedule: task not scheduled", "task", t.Name, "error", err)
			continue
		}
		task := t
		id, err := r.cron.AddFunc(t.Cron, func() { r.fire(ctx, task) })
		if err != nil {
			slog.Warn("schedule: task not scheduled", "task", t.Name, "error", err)
			continue
		}
		r.entries[t.Name] = id
	}
	for name, id := range r.entries {
		if !seen[name] {
			r.cron.Remove(id)
			delete(r.entries, name)
			delete(r.tasks, name)
		}
	}
	return nil
}

func (r *Runner) fire(ctx context.Context, t Task) {
	r.mu.Lock()
	if r.running[t.Name] {
		r.mu.Unlock()
		slog.Warn("schedule: task still running, this time skipped", "task", t.Name)
		return
	}
	r.running[t.Name] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, t.Name)
		r.mu.Unlock()
	}()

	runCtx := ctx
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	started := time.Now()
	answer, err := r.Run(runCtx, t)
	if err != nil {
		slog.Warn("schedule: task failed", "task", t.Name, "error", err, "duration_ms", time.Since(started).Milliseconds())
	} else {
		slog.Info("schedule: task ran", "task", t.Name, "answer_bytes", len(answer), "duration_ms", time.Since(started).Milliseconds())
	}
	if r.Deliver != nil {
		r.Deliver(ctx, t, answer, err)
	}
}
