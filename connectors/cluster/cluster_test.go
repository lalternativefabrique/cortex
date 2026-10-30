package cluster

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/host"
)

func broker(t *testing.T) string {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Port: -1, JetStream: true, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func connect(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func instance(t *testing.T, url string, cfg Config, run host.Runner) *Turns {
	t.Helper()
	turns, err := NewTurns(context.Background(), connect(t, url), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if run != nil {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = turns.Serve(ctx, run); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	return turns
}

func turnOf(contextID string) host.Turn {
	return host.Turn{TaskID: a2a.NewTaskID(), ContextID: contextID, Message: &a2a.Message{Metadata: map[string]any{host.SubjectKey: "p"}}}
}

func answering(text string) host.Runner {
	return func(ctx context.Context, turn host.Turn, emit host.Emit) error {
		if err := emit(ctx, a2a.NewArtifactEvent(turn, a2a.TextPart{Text: text})); err != nil {
			return err
		}
		ev := a2a.NewStatusUpdateEvent(turn, a2a.TaskStateCompleted, nil)
		ev.Final = true
		return emit(ctx, ev)
	}
}

func collect(events *[]a2a.Event, mu *sync.Mutex) host.Emit {
	return func(_ context.Context, ev a2a.Event) error {
		mu.Lock()
		defer mu.Unlock()
		*events = append(*events, ev)
		return nil
	}
}

func TestATurnDispatchedOnOneInstanceRunsOnAnotherAndStreamsBack(t *testing.T) {
	url := broker(t)
	front := instance(t, url, Config{Agent: "cerveau"}, nil)
	instance(t, url, Config{Agent: "cerveau", Concurrency: 2}, answering("bonjour"))

	var events []a2a.Event
	var mu sync.Mutex
	if err := front.Dispatch(context.Background(), turnOf("c1"), collect(&events, &mu)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events", len(events))
	}
	art, ok := events[0].(*a2a.TaskArtifactUpdateEvent)
	if !ok || art.Artifact.Parts[0].(a2a.TextPart).Text != "bonjour" {
		t.Fatalf("first event %#v", events[0])
	}
	if st, ok := events[1].(*a2a.TaskStatusUpdateEvent); !ok || !st.Final {
		t.Fatalf("last event %#v", events[1])
	}
}

func TestTurnsSpreadOverInstancesWithinEachOnesConcurrency(t *testing.T) {
	url := broker(t)
	var running, most atomic.Int32
	var byInstance [2]atomic.Int32
	slow := func(i int) host.Runner {
		return func(ctx context.Context, turn host.Turn, emit host.Emit) error {
			byInstance[i].Add(1)
			n := running.Add(1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			time.Sleep(100 * time.Millisecond)
			running.Add(-1)
			return answering("ok")(ctx, turn, emit)
		}
	}
	front := instance(t, url, Config{Agent: "a"}, nil)
	instance(t, url, Config{Agent: "a", Concurrency: 2}, slow(0))
	instance(t, url, Config{Agent: "a", Concurrency: 2}, slow(1))

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var events []a2a.Event
			var mu sync.Mutex
			if err := front.Dispatch(context.Background(), turnOf(string(rune('a'+i))), collect(&events, &mu)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if most.Load() > 4 {
		t.Fatalf("%d turns ran at once over two instances of two", most.Load())
	}
	if byInstance[0].Load() == 0 || byInstance[1].Load() == 0 {
		t.Fatalf("turns did not spread: %d / %d", byInstance[0].Load(), byInstance[1].Load())
	}
}

func TestTwoTurnsOfOneConversationNeverRunAtOnceAcrossInstances(t *testing.T) {
	url := broker(t)
	var running atomic.Int32
	var overlapped atomic.Bool
	run := func(ctx context.Context, turn host.Turn, emit host.Emit) error {
		if running.Add(1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(50 * time.Millisecond)
		running.Add(-1)
		return answering("ok")(ctx, turn, emit)
	}
	front := instance(t, url, Config{Agent: "a"}, nil)
	instance(t, url, Config{Agent: "a", Concurrency: 4}, run)
	instance(t, url, Config{Agent: "a", Concurrency: 4}, run)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var events []a2a.Event
			var mu sync.Mutex
			if err := front.Dispatch(context.Background(), turnOf("same"), collect(&events, &mu)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if overlapped.Load() {
		t.Fatal("two turns of one conversation ran at once")
	}
}

func TestATurnNoInstanceTakesIsRefusedAsBusy(t *testing.T) {
	url := broker(t)
	front := instance(t, url, Config{Agent: "a", QueueWait: 200 * time.Millisecond}, nil)
	err := front.Dispatch(context.Background(), turnOf("c"), func(context.Context, a2a.Event) error { return nil })
	if !errors.Is(err, host.ErrBusy) {
		t.Fatalf("got %v, want ErrBusy", err)
	}
}

func TestACallerThatLeavesStopsTheTurnOnTheInstanceRunningIt(t *testing.T) {
	url := broker(t)
	stopped := make(chan struct{})
	front := instance(t, url, Config{Agent: "a"}, nil)
	instance(t, url, Config{Agent: "a"}, func(ctx context.Context, turn host.Turn, emit host.Emit) error {
		_ = emit(ctx, a2a.NewStatusUpdateEvent(turn, a2a.TaskStateWorking, nil))
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	ctx, leave := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		leave()
	}()
	_ = front.Dispatch(ctx, turnOf("c"), func(context.Context, a2a.Event) error { return nil })
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("the running turn did not stop")
	}
}

func TestAConversationIsReadByAnyInstance(t *testing.T) {
	url := broker(t)
	js := func() jetstream.JetStream {
		j, err := jetstream.New(connect(t, url))
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	one, err := NewHistory(context.Background(), js(), "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewHistory(context.Background(), js(), "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = one.Append(ctx, "p\x00c", agent.Message{Role: agent.RoleUser, Content: "premier"}, agent.Message{Role: agent.RoleAssistant, Content: "réponse"})
	got, err := two.Load(ctx, "p\x00c")
	if err != nil || len(got) != 2 || got[0].Content != "premier" {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestAConversationPastItsSizeDropsItsOldestExchanges(t *testing.T) {
	url := broker(t)
	j, _ := jetstream.New(connect(t, url))
	h, err := NewHistory(context.Background(), j, "a", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	big := strings.Repeat("x", 200<<10)
	for i := range 6 {
		if err := h.Append(ctx, "k", agent.Message{Role: agent.RoleUser, Content: big}, agent.Message{Role: agent.RoleAssistant, Content: string(rune('0' + i))}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := h.Load(ctx, "k")
	if len(got) >= 12 || got[len(got)-1].Content != "5" {
		t.Fatalf("kept %d messages, last %q", len(got), got[len(got)-1].Content)
	}
}

func TestASkillsRefreshReachesTheOtherInstancesOnly(t *testing.T) {
	url := broker(t)
	var selfReloads, otherReloads atomic.Int32
	self, stopSelf, err := NewSkillsRefresh(connect(t, url), "a", "i1", func(context.Context) { selfReloads.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer stopSelf()
	nc := connect(t, url)
	_, stopOther, err := NewSkillsRefresh(nc, "a", "i2", func(context.Context) { otherReloads.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer stopOther()
	_ = nc.Flush()
	self.Announce(context.Background())
	deadline := time.Now().Add(2 * time.Second)
	for otherReloads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if otherReloads.Load() != 1 || selfReloads.Load() != 0 {
		t.Fatalf("self %d, other %d", selfReloads.Load(), otherReloads.Load())
	}
}
