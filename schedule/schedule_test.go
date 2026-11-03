package schedule

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTheFileStoreKeepsTasksAndSeesHandEdits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cfg", "schedule.yaml")
	store := NewFileStore(path)
	if err := store.Put(ctx, Task{Name: "seo-monday", Cron: "0 9 * * 1", Prompt: "Check the rankings."}); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, Task{Name: "bad", Cron: "every monday", Prompt: "x"}); err == nil {
		t.Fatal("a cron that does not parse was accepted")
	}
	if err := store.Put(ctx, Task{Name: "seo-monday", Cron: "0 10 * * 1", Prompt: "Check the rankings, then the AI answers."}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := store.List(ctx)
	if len(tasks) != 1 || tasks[0].Cron != "0 10 * * 1" {
		t.Fatalf("tasks = %+v", tasks)
	}

	raw, _ := os.ReadFile(path)
	edited := strings.Replace(string(raw), "cron: 0 10 * * 1", "cron: '@daily'", 1) + "- name: digest\n  cron: '@hourly'\n  prompt: Summarise the news.\n  paused: true\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.List(ctx)
	if err != nil || len(tasks) != 2 || tasks[0].Cron != "@daily" || !tasks[1].Paused {
		t.Fatalf("tasks = %+v, err = %v", tasks, err)
	}
	if err := store.Remove(ctx, "digest"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(ctx, "digest"); err == nil {
		t.Fatal("removing a missing task must say so")
	}
}

type memStore struct {
	mu    sync.Mutex
	tasks []Task
}

func (m *memStore) List(context.Context) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Task(nil), m.tasks...), nil
}
func (m *memStore) Put(_ context.Context, t Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tasks {
		if m.tasks[i].Name == t.Name {
			m.tasks[i] = t
			return nil
		}
	}
	m.tasks = append(m.tasks, t)
	return nil
}
func (m *memStore) Remove(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tasks {
		if m.tasks[i].Name == name {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			return nil
		}
	}
	return nil
}

func TestTheRunnerFiresATaskInItsOwnConversationAndDelivers(t *testing.T) {
	store := &memStore{tasks: []Task{{Name: "tick", Cron: "@every 1s", Prompt: "Say tick."}}}
	var mu sync.Mutex
	var runs, delivered []string
	r := &Runner{
		Store: store,
		Run: func(_ context.Context, task Task) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			runs = append(runs, task.Conversation()+"|"+task.Prompt)
			return "tick", nil
		},
		Deliver: func(_ context.Context, task Task, answer string, err error) {
			mu.Lock()
			defer mu.Unlock()
			delivered = append(delivered, task.Name+"="+answer)
		},
		Reload: time.Hour,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	_ = r.Serve(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(runs) < 1 || runs[0] != "task:tick|Say tick." || len(delivered) != len(runs) || delivered[0] != "tick=tick" {
		t.Fatalf("runs = %v, delivered = %v", runs, delivered)
	}
}

func TestRefreshFollowsTheStoreAndSkipsPausedTasks(t *testing.T) {
	store := &memStore{tasks: []Task{{Name: "a", Cron: "@daily", Prompt: "a"}, {Name: "p", Cron: "@daily", Prompt: "p", Paused: true}}}
	r := &Runner{Store: store, Run: func(context.Context, Task) (string, error) { return "", nil }, Reload: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.Serve(ctx) }()
	defer cancel()
	time.Sleep(50 * time.Millisecond)

	r.mu.Lock()
	n := len(r.entries)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("scheduled = %d: one task, the paused one left out", n)
	}
	_ = store.Remove(ctx, "a")
	_ = store.Put(ctx, Task{Name: "b", Cron: "@hourly", Prompt: "b"})
	if err := r.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	_, hasA := r.entries["a"]
	_, hasB := r.entries["b"]
	r.mu.Unlock()
	if hasA || !hasB {
		t.Fatalf("entries after refresh: a=%v b=%v", hasA, hasB)
	}
}

func TestTheToolsLetTheAgentManageItsTasks(t *testing.T) {
	ctx := context.Background()
	store := &memStore{}
	refreshed := 0
	tools := Tools(store, time.UTC, func(context.Context) { refreshed++ })
	byName := map[string]func(json.RawMessage) string{}
	for _, tool := range tools {
		tool := tool
		byName[tool.Name()] = func(args json.RawMessage) string {
			res, _ := tool.Execute(ctx, args)
			return res.Content
		}
	}
	if out := byName["schedule_task"](json.RawMessage(`{"name":"seo-monday","cron":"0 9 * * 1","prompt":"Check the rankings."}`)); !strings.HasPrefix(out, `scheduled "seo-monday" (0 9 * * 1); next run Mon`) {
		t.Fatalf("schedule: %s", out)
	}
	if out := byName["schedule_task"](json.RawMessage(`{"name":"x","cron":"tomorrow","prompt":"y"}`)); !strings.HasPrefix(out, "error:") {
		t.Fatalf("a bad cron must be refused: %s", out)
	}
	if out := byName["list_scheduled_tasks"](nil); !strings.Contains(out, "- seo-monday (0 9 * * 1) next Mon") || !strings.Contains(out, "Check the rankings.") {
		t.Fatalf("list: %s", out)
	}
	if out := byName["unschedule_task"](json.RawMessage(`{"name":"seo-monday"}`)); out != `removed "seo-monday"` {
		t.Fatalf("unschedule: %s", out)
	}
	if out := byName["list_scheduled_tasks"](nil); out != "no task is scheduled" {
		t.Fatalf("list after removal: %s", out)
	}
	if refreshed != 2 {
		t.Fatalf("refreshed %d times: once per write", refreshed)
	}
}
