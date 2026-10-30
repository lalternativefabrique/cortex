package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolExchange builds the assistant turn and tool reply for one call.
func toolExchange(id, name, args, result string) []Message {
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(args)}}},
		{Role: RoleTool, ToolCallID: id, Content: result},
	}
}

func padding(n int) []Message {
	out := make([]Message, n)
	for i := range out {
		out[i] = Message{Role: RoleAssistant, Content: "thinking"}
	}
	return out
}

func TestEvictReplacesSupersededRead(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, Message{Role: RoleUser, Content: "fix it"})
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go"}`, strings.Repeat("old content\n", 50))...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"a.go"}`, "new content")...)
	msgs = append(msgs, padding(10)...)

	out, freed := EvictStale(msgs, 4)
	if freed <= 0 {
		t.Fatal("nothing was evicted despite a superseded read")
	}
	if out[2].Content == msgs[2].Content {
		t.Fatal("the stale read was left in place")
	}
	if !strings.Contains(out[2].Content, "superseded") {
		t.Fatalf("the replacement does not say what happened: %q", out[2].Content)
	}
	if out[4].Content != "new content" {
		t.Fatal("the current read was evicted instead of the stale one")
	}
}

func TestEvictKeepsMessagesInPlace(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go"}`, "old")...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"a.go"}`, "new")...)
	msgs = append(msgs, padding(10)...)

	out, _ := EvictStale(msgs, 2)
	if len(out) != len(msgs) {
		t.Fatalf("message count changed %d -> %d; dropping a tool message orphans its assistant turn", len(msgs), len(out))
	}
	if out[1].Role != RoleTool || out[1].ToolCallID != "c1" {
		t.Fatal("the evicted message lost its identity")
	}
}

func TestEvictSparesRecentResults(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go"}`, "old")...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"a.go"}`, "new")...)

	// Both exchanges sit inside the recent window.
	out, freed := EvictStale(msgs, 8)
	if freed != 0 {
		t.Fatal("a result inside the recent window was evicted")
	}
	if out[1].Content != "old" {
		t.Fatal("recent history was rewritten")
	}
}

func TestEvictLeavesBashResultsAlone(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "bash", `{"command":"go test ./..."}`, "FAIL: 3 tests")...)
	msgs = append(msgs, toolExchange("c2", "bash", `{"command":"go test ./..."}`, "ok")...)
	msgs = append(msgs, padding(10)...)

	_, freed := EvictStale(msgs, 2)
	if freed != 0 {
		t.Fatal("a bash result was evicted; two runs of a command are different observations, not a stale snapshot")
	}
}

func TestEvictDistinguishesPagesOfTheSameFile(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go","offset":1,"limit":100}`, "first page")...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"a.go","offset":101,"limit":100}`, "second page")...)
	msgs = append(msgs, padding(10)...)

	_, freed := EvictStale(msgs, 2)
	if freed != 0 {
		t.Fatal("a different page of the same file is not a replacement")
	}
}

func TestEvictDistinguishesDifferentFiles(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go"}`, "content of a")...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"b.go"}`, "content of b")...)
	msgs = append(msgs, padding(10)...)

	_, freed := EvictStale(msgs, 2)
	if freed != 0 {
		t.Fatal("reads of different files were treated as superseding each other")
	}
}

func TestEvictHandlesRepeatedSearches(t *testing.T) {
	var msgs []Message
	for _, id := range []string{"c1", "c2", "c3"} {
		msgs = append(msgs, toolExchange(id, "grep", `{"pattern":"Transfer","path":""}`, strings.Repeat("hit\n", 30))...)
	}
	msgs = append(msgs, padding(10)...)

	out, freed := EvictStale(msgs, 2)
	if freed <= 0 {
		t.Fatal("repeated identical searches were all kept")
	}
	evicted := 0
	for _, m := range out {
		if m.Content == evictionNotice {
			evicted++
		}
	}
	if evicted != 2 {
		t.Fatalf("evicted %d of 3 searches, want the 2 older ones", evicted)
	}
}

func TestEvictIsIdempotent(t *testing.T) {
	var msgs []Message
	msgs = append(msgs, toolExchange("c1", "read", `{"path":"a.go"}`, strings.Repeat("old\n", 50))...)
	msgs = append(msgs, toolExchange("c2", "read", `{"path":"a.go"}`, "new")...)
	msgs = append(msgs, padding(10)...)

	once, freed := EvictStale(msgs, 2)
	if freed <= 0 {
		t.Fatal("nothing evicted on the first pass")
	}
	_, again := EvictStale(once, 2)
	if again != 0 {
		t.Fatalf("a second pass freed %d more tokens; eviction should converge", again)
	}
}

func TestEvictLeavesCleanHistoryUntouched(t *testing.T) {
	msgs := append([]Message{{Role: RoleUser, Content: "go"}},
		toolExchange("c1", "read", `{"path":"a.go"}`, "content")...)

	out, freed := EvictStale(msgs, 0)
	if freed != 0 {
		t.Fatal("a history with no superseded results was modified")
	}
	if len(out) != len(msgs) {
		t.Fatal("message count changed")
	}
}

func TestEvictToleratesOrphanToolMessage(t *testing.T) {
	// A tool message whose assistant turn was compacted away must not crash
	// the walk.
	msgs := []Message{
		{Role: RoleUser, Content: "go"},
		{Role: RoleTool, ToolCallID: "gone", Content: "result"},
	}
	if _, freed := EvictStale(msgs, 0); freed != 0 {
		t.Fatal("an orphan tool message was evicted on no evidence")
	}
}

func TestRunEvictsAcrossSteps(t *testing.T) {
	tool := &fakeTool{name: "read", result: strings.Repeat("file content line\n", 200)}
	call := CompletionResponse{ToolCalls: []ToolCall{
		{ID: "c", Name: "read", Arguments: json.RawMessage(`{"path":"a.go"}`)},
	}}
	client := &scriptedClient{responses: []CompletionResponse{
		call, call, call, call, call, {Text: "done"},
	}}
	var evicted int
	r, err := NewRunner(Config{
		Client:          client,
		Tools:           []Tool{tool},
		MaxSteps:        8,
		ContextWindow:   100000,
		EvictKeepRecent: 2,
		Callback:        &countingCallback{onEvict: func(int) { evicted++ }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(t.Context(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	if evicted == 0 {
		t.Fatal("repeated identical reads accumulated without eviction")
	}
}

type countingCallback struct {
	NopCallback
	onEvict func(int)
}

func (c *countingCallback) OnEvict(freed int) { c.onEvict(freed) }
