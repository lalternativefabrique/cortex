package host

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternative/packages/go/cortex/agent"
)

func turnOf(contextID string) Turn {
	return Turn{TaskID: a2a.NewTaskID(), ContextID: contextID, Message: &a2a.Message{}}
}

func noEmit(context.Context, a2a.Event) error { return nil }

func TestLocalTurnsRunNoMoreThanTheirConcurrencyAtOnce(t *testing.T) {
	var running, most atomic.Int32
	turns := NewLocalTurns(2, time.Minute)
	turns.bind(func(ctx context.Context, _ Turn, _ Emit) error {
		n := running.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return nil
	})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := turns.Dispatch(context.Background(), turnOf(string(rune('a'+i))), noEmit); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() != 2 {
		t.Fatalf("at most %d turns ran at once, want 2", most.Load())
	}
}

func TestTheTurnsOfOneConversationRunOneAfterTheOther(t *testing.T) {
	var running atomic.Int32
	var overlapped atomic.Bool
	turns := NewLocalTurns(8, time.Minute)
	turns.bind(func(ctx context.Context, _ Turn, _ Emit) error {
		if running.Add(1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(10 * time.Millisecond)
		running.Add(-1)
		return nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = turns.Dispatch(context.Background(), turnOf("same"), noEmit)
		}()
	}
	wg.Wait()
	if overlapped.Load() {
		t.Fatal("two turns of one conversation ran at once")
	}
}

func TestATurnThatFindsNoRoomInTimeIsRefusedAsBusy(t *testing.T) {
	release := make(chan struct{})
	turns := NewLocalTurns(1, 30*time.Millisecond)
	turns.bind(func(context.Context, Turn, Emit) error { <-release; return nil })
	go func() { _ = turns.Dispatch(context.Background(), turnOf("first"), noEmit) }()
	time.Sleep(10 * time.Millisecond)
	err := turns.Dispatch(context.Background(), turnOf("second"), noEmit)
	close(release)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

func TestAReasoningEffortComesFromTheRunThenTheAgentThenTheModel(t *testing.T) {
	cases := []struct {
		meta     map[string]any
		provider agent.Provider
		want     string
	}{
		{map[string]any{ReasoningEffortKey: "low"}, agent.Provider{ReasoningEffort: "high", Model: "deepseek-v4"}, "low"},
		{nil, agent.Provider{ReasoningEffort: "medium", Model: "deepseek-v4"}, "medium"},
		{nil, agent.Provider{Model: "deepseek-v4-flash"}, agent.ReasoningEffortNone},
		{nil, agent.Provider{Model: "mistral-medium"}, ""},
	}
	for _, c := range cases {
		got, err := reasoningEffort(c.meta, c.provider)
		if err != nil || got != c.want {
			t.Errorf("%v %+v: got %q %v, want %q", c.meta, c.provider, got, err, c.want)
		}
	}
	if _, err := reasoningEffort(map[string]any{ReasoningEffortKey: "max"}, agent.Provider{}); err == nil {
		t.Error("an unknown effort was accepted")
	}
}

func TestALentTokenPastItsExpiryIsSeenExpired(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	past := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":999999}`)) + ".s"
	future := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1000001}`)) + ".s"
	if !turnTokenExpired(past, now) || turnTokenExpired(future, now) || turnTokenExpired("opaque", now) {
		t.Fatal("expiry misread")
	}
}

func TestATurnWithoutRecallNeitherOffersNorWritesMemory(t *testing.T) {
	provider, bodies := fakeModel(t, "")
	mem := &memory{}
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t", Recall: mem})
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-r", map[string]any{SubjectKey: "p1", RecallKey: false})))
	if strings.Contains((*bodies)[0], "recall_memory") {
		t.Fatal("recall_memory was offered")
	}
	time.Sleep(50 * time.Millisecond)
	mem.mu.Lock()
	defer mem.mu.Unlock()
	if len(mem.entries) != 0 {
		t.Fatalf("memory written: %v", mem.entries)
	}
}

func TestTheRunsReasoningEffortReachesTheModel(t *testing.T) {
	provider, bodies := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t"})
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("vite", "ctx-e", map[string]any{ReasoningEffortKey: "low"})))
	if !strings.Contains((*bodies)[0], `"reasoning_effort":"low"`) {
		t.Fatalf("model got %s", (*bodies)[0])
	}
}
