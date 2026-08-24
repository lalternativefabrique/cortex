package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func longHistory(turns int) []Message {
	msgs := []Message{{Role: RoleUser, Content: "fix the failing test in pkg/foo"}}
	for i := range turns {
		msgs = append(msgs,
			Message{
				Role:      RoleAssistant,
				ToolCalls: []ToolCall{{ID: "c", Name: "bash", Arguments: json.RawMessage(`{"command":"go test ./..."}`)}},
			},
			Message{Role: RoleTool, ToolCallID: "c", Content: strings.Repeat("output line\n", 20+i)},
		)
	}
	return msgs
}

func TestSummaryCompactorKeepsTaskAndRecentTurns(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "the summary"}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, KeepFirst: 1}

	in := longHistory(10)
	out, err := c.Compact(context.Background(), "sys", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) >= len(in) {
		t.Fatalf("compaction produced %d messages from %d", len(out), len(in))
	}
	if out[0].Content != in[0].Content {
		t.Fatal("the opening task statement was dropped")
	}
	if !strings.Contains(out[1].Content, "the summary") {
		t.Fatalf("summary is missing: %q", out[1].Content)
	}
	if out[len(out)-1].Content != in[len(in)-1].Content {
		t.Fatal("the most recent turn was not kept verbatim")
	}
}

func TestSummaryCompactorLeavesShortHistoryAlone(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "unused"}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 6, KeepFirst: 1}

	in := longHistory(2)
	out, err := c.Compact(context.Background(), "sys", in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(in) {
		t.Fatalf("short history was compacted: %d -> %d", len(in), len(out))
	}
	if len(client.calls) != 0 {
		t.Fatal("a summarisation call was made for a history that fits")
	}
}

func TestSummaryCompactorDropsOrphanToolMessages(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "summary"}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 1, KeepFirst: 1}

	// The kept tail starts on a tool message whose assistant turn is cut.
	in := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "orphan", Name: "bash"}}},
		{Role: RoleTool, ToolCallID: "orphan", Content: "result"},
	}
	out, err := c.Compact(context.Background(), "sys", in)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out {
		if m.Role == RoleTool {
			t.Fatalf("an orphan tool message survived: %+v", m)
		}
	}
}

func TestSummaryCompactorFailsWhenModelReturnsNothing(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "   "}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 2, KeepFirst: 1}
	if _, err := c.Compact(context.Background(), "sys", longHistory(10)); err == nil {
		t.Fatal("an empty summary was accepted, which would silently erase the history")
	}
}

func TestRunCompactsWhenWindowFills(t *testing.T) {
	tool := &fakeTool{name: "probe", result: strings.Repeat("verbose tool output\n", 400)}
	looping := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{
		looping, looping, looping, looping, {Text: "done"},
	}}
	r, err := NewRunner(Config{
		Client:        client,
		Tools:         []Tool{tool},
		MaxSteps:      6,
		ContextWindow: 2000,
		Compactor:     &stubCompactor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compactions == 0 {
		t.Fatal("history grew past the window without compacting")
	}
}

func TestRunDoesNotCompactWithoutContextWindow(t *testing.T) {
	tool := &fakeTool{name: "probe", result: strings.Repeat("noise\n", 500)}
	looping := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{looping, looping, {Text: "done"}}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxSteps: 4})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compactions != 0 {
		t.Fatal("compaction ran with no context window configured")
	}
}

func TestRunSurvivesCompactionFailure(t *testing.T) {
	tool := &fakeTool{name: "probe", result: strings.Repeat("noise\n", 400)}
	looping := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{looping, looping, {Text: "done"}}}
	r, err := NewRunner(Config{
		Client:        client,
		Tools:         []Tool{tool},
		MaxSteps:      4,
		ContextWindow: 1000,
		Compactor:     failingCompactor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatalf("a failed compaction aborted the run: %v", err)
	}
	if res.Text != "done" {
		t.Fatalf("Text = %q, want the run to have completed", res.Text)
	}
}

type stubCompactor struct{}

func (stubCompactor) Compact(_ context.Context, _ string, messages []Message) ([]Message, error) {
	if len(messages) <= 2 {
		return messages, nil
	}
	return []Message{messages[0], {Role: RoleUser, Content: "[compacted]"}}, nil
}

type failingCompactor struct{}

func (failingCompactor) Compact(context.Context, string, []Message) ([]Message, error) {
	return nil, context.DeadlineExceeded
}
