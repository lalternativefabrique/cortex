package cluster

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternative/packages/go/cortex/host"
)

func TestALongAnswerReachesASlowCallerWhole(t *testing.T) {
	url := broker(t)
	const chunks = 3000
	front := instance(t, url, Config{Agent: "a", Silence: 3 * time.Second}, nil)
	instance(t, url, Config{Agent: "a"}, func(ctx context.Context, turn host.Turn, emit host.Emit) error {
		id := a2a.NewArtifactID()
		for range chunks {
			if err := emit(ctx, a2a.NewArtifactUpdateEvent(turn, id, a2a.TextPart{Text: "x"})); err != nil {
				return err
			}
		}
		ev := a2a.NewStatusUpdateEvent(turn, a2a.TaskStateCompleted, nil)
		ev.Final = true
		return emit(ctx, ev)
	})
	var mu sync.Mutex
	got := 0
	err := front.Dispatch(context.Background(), turnOf("c"), func(context.Context, a2a.Event) error {
		time.Sleep(200 * time.Microsecond)
		mu.Lock()
		got++
		mu.Unlock()
		return nil
	})
	if err != nil || got != chunks+1 {
		t.Fatalf("got %d of %d events, err %v", got, chunks+1, err)
	}
}

func TestAnInstanceStoppingFinishesTheTurnsItRuns(t *testing.T) {
	url := broker(t)
	front := instance(t, url, Config{Agent: "a"}, nil)
	worker, err := NewTurns(context.Background(), connect(t, url), Config{Agent: "a"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan struct{})
	running := make(chan struct{})
	go func() {
		_ = worker.Serve(ctx, func(ctx context.Context, turn host.Turn, emit host.Emit) error {
			close(running)
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			ev := a2a.NewStatusUpdateEvent(turn, a2a.TaskStateCompleted, nil)
			ev.Final = true
			return emit(ctx, ev)
		})
		close(served)
	}()
	go func() { <-running; stop() }()
	var final bool
	err = front.Dispatch(context.Background(), turnOf("c"), func(_ context.Context, ev a2a.Event) error {
		if st, ok := ev.(*a2a.TaskStatusUpdateEvent); ok && st.Final && st.Status.State == a2a.TaskStateCompleted {
			final = true
		}
		return nil
	})
	<-served
	if err != nil || !final {
		t.Fatalf("turn cut by the stop: completed=%v err=%v", final, err)
	}
}

func TestATaskIDThatWouldWidenASubjectIsRefused(t *testing.T) {
	url := broker(t)
	front := instance(t, url, Config{Agent: "a", QueueWait: 200 * time.Millisecond}, nil)
	for _, id := range []string{"x.>", "*", "a b", ""} {
		turn := turnOf("c")
		turn.TaskID = a2a.TaskID(id)
		err := front.Dispatch(context.Background(), turn, func(context.Context, a2a.Event) error { return nil })
		if err == nil || err.Error() == host.ErrBusy.Error() {
			t.Errorf("task id %q: got %v, want a refusal", id, err)
		}
	}
}
