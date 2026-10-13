package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type scriptedClient struct {
	responses []CompletionResponse
	err       error
	mu        sync.Mutex
	calls     []CompletionRequest
}

func (c *scriptedClient) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if c.err != nil {
		return CompletionResponse{}, c.err
	}
	if len(c.calls) > len(c.responses) {
		return CompletionResponse{Text: "done"}, nil
	}
	return c.responses[len(c.calls)-1], nil
}

type fakeTool struct {
	name    string
	result  string
	err     error
	mu      sync.Mutex
	invoked int
}

func (f *fakeTool) Name() string        { return f.name }
func (f *fakeTool) Description() string { return "fake" }
func (f *fakeTool) InputSchema() any    { return struct{}{} }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (ToolResult, error) {
	f.mu.Lock()
	f.invoked++
	f.mu.Unlock()
	if f.err != nil {
		return ToolResult{}, f.err
	}
	return ToolResult{Content: f.result}, nil
}

func TestRunStopsWhenModelEmitsNoToolCalls(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "final answer"}}}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "final answer" {
		t.Fatalf("Text = %q, want %q", res.Text, "final answer")
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
	if res.Truncated {
		t.Fatal("Truncated = true, want false")
	}
}

func TestRunFeedsToolResultBackAndContinues(t *testing.T) {
	tool := &fakeTool{name: "probe", result: "tool output"}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "call-1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{Text: "answered"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps != 2 {
		t.Fatalf("Steps = %d, want 2", res.Steps)
	}
	if tool.invoked != 1 {
		t.Fatalf("tool invoked %d times, want 1", tool.invoked)
	}

	second := client.calls[1].Messages
	last := second[len(second)-1]
	if last.Role != RoleTool {
		t.Fatalf("last message role = %q, want %q", last.Role, RoleTool)
	}
	if last.ToolCallID != "call-1" {
		t.Fatalf("ToolCallID = %q, want call-1", last.ToolCallID)
	}
	if last.Content != "tool output" {
		t.Fatalf("Content = %q, want %q", last.Content, "tool output")
	}
}

func TestRunReportsToolErrorToModelWithoutFailing(t *testing.T) {
	tool := &fakeTool{name: "probe", err: errors.New("disk on fire")}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{Text: "recovered"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatalf("Run returned error %v, want the tool failure reported as a result", err)
	}
	if res.Text != "recovered" {
		t.Fatalf("Text = %q, want %q", res.Text, "recovered")
	}
	fed := client.calls[1].Messages
	if got := fed[len(fed)-1].Content; !strings.Contains(got, "disk on fire") {
		t.Fatalf("tool message = %q, want it to carry the failure", got)
	}
}

func TestRunRefusesUnknownToolAsResult(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c", Name: "ghost", Arguments: json.RawMessage(`{}`)}}},
		{Text: "ok"},
	}}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	fed := client.calls[1].Messages
	if got := fed[len(fed)-1].Content; !strings.Contains(got, "unknown tool") {
		t.Fatalf("tool message = %q, want an unknown-tool notice", got)
	}
}

func TestRunStopsAtMaxStepsAndReportsTruncation(t *testing.T) {
	tool := &fakeTool{name: "probe", result: "again"}
	looping := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{looping, looping, looping}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxSteps: 3, StallAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("Truncated = false, want true")
	}
	if res.Steps != 3 {
		t.Fatalf("Steps = %d, want 3", res.Steps)
	}
}

func TestRunKeepsToolMessagesInRequestOrder(t *testing.T) {
	slow := &fakeTool{name: "slow", result: "slow result"}
	fast := &fakeTool{name: "fast", result: "fast result"}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{
			{ID: "first", Name: "slow", Arguments: json.RawMessage(`{}`)},
			{ID: "second", Name: "fast", Arguments: json.RawMessage(`{}`)},
		}},
		{Text: "ok"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{slow, fast}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	msgs := client.calls[1].Messages
	toolMsgs := msgs[len(msgs)-2:]
	if toolMsgs[0].ToolCallID != "first" || toolMsgs[1].ToolCallID != "second" {
		t.Fatalf("tool messages out of order: %q then %q", toolMsgs[0].ToolCallID, toolMsgs[1].ToolCallID)
	}
}

func TestRunAccumulatesUsageAcrossSteps(t *testing.T) {
	tool := &fakeTool{name: "probe", result: "x"}
	client := &scriptedClient{responses: []CompletionResponse{
		{
			ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}},
			Usage:     Usage{Input: 100, Output: 10},
		},
		{Text: "done", Usage: Usage{Input: 150, Output: 20, CachedInput: 100}},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Input: 250, Output: 30, CachedInput: 100}
	if res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", res.Usage, want)
	}
}

func TestRunTruncatesOversizedToolResult(t *testing.T) {
	tool := &fakeTool{name: "probe", result: strings.Repeat("line of output\n", 2000)}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{Text: "ok"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxToolResultBytes: 500})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	fed := client.calls[1].Messages
	content := fed[len(fed)-1].Content
	if len(content) > 500 {
		t.Fatalf("tool message is %d bytes, want at most 500", len(content))
	}
	if !strings.Contains(content, "truncated") {
		t.Fatal("truncated tool message does not say so")
	}
}

func TestNewRunnerRejectsDuplicateToolNames(t *testing.T) {
	_, err := NewRunner(Config{
		Client: &scriptedClient{},
		Tools:  []Tool{&fakeTool{name: "dup"}, &fakeTool{name: "dup"}},
	})
	if err == nil {
		t.Fatal("NewRunner accepted duplicate tool names")
	}
}

func TestRunDoesNotMutateCallerMessages(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "done"}}}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	messages := []Message{{Role: RoleUser, Content: "hi"}}
	if _, err := r.Run(context.Background(), messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("caller slice grew to %d messages", len(messages))
	}
}

func TestRunStopsOnTheInputTokenBudgetWithWhatTheModelSaid(t *testing.T) {
	tool := &fakeTool{name: "probe", result: "again"}
	calling := CompletionResponse{
		Text:      "still looking",
		ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{"q":1}`)}},
		Usage:     Usage{Input: 600, Output: 5},
	}
	client := &scriptedClient{responses: []CompletionResponse{calling, calling, calling}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxInputTokens: 1000, StallAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exhausted || res.Truncated {
		t.Fatalf("Exhausted = %v, Truncated = %v, want exhausted only", res.Exhausted, res.Truncated)
	}
	if res.Steps != 2 || res.Usage.Input != 1200 {
		t.Fatalf("Steps = %d, Usage.Input = %d, want 2 steps and 1200 tokens", res.Steps, res.Usage.Input)
	}
	if res.Text != "still looking" || tool.invoked != 1 {
		t.Fatalf("Text = %q, invoked = %d: the step that crossed the budget must not run its tools", res.Text, tool.invoked)
	}
}

func TestRunStallsWhenAStepRepeatsThePreviousOne(t *testing.T) {
	tool := &fakeTool{name: "search", result: "nothing"}
	same := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "search", Arguments: json.RawMessage(`{"q":"x"}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{same, same, same, same}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stalled || res.Truncated {
		t.Fatalf("Stalled = %v, Truncated = %v, want stalled only", res.Stalled, res.Truncated)
	}
	if tool.invoked != 1 {
		t.Fatalf("invoked = %d, want 1: a repeated call is answered with a notice, not run", tool.invoked)
	}
	if res.Steps != 3 || res.Repeats != 2 {
		t.Fatalf("Steps = %d, Repeats = %d, want 3 and 2", res.Steps, res.Repeats)
	}
	msgs := client.calls[2].Messages
	last := msgs[len(msgs)-1]
	if last.Role != RoleTool || last.Content != repeatedCallNotice {
		t.Fatalf("the model was not told its call was a repeat: %+v", last)
	}
}

func TestRunDoesNotStallWhenCallsAlternate(t *testing.T) {
	test := &fakeTool{name: "test", result: "FAIL"}
	edit := &fakeTool{name: "edit", result: "ok"}
	call := func(name string) CompletionResponse {
		return CompletionResponse{ToolCalls: []ToolCall{{ID: name, Name: name, Arguments: json.RawMessage(`{}`)}}}
	}
	client := &scriptedClient{responses: []CompletionResponse{call("test"), call("edit"), call("test"), call("edit"), call("test"), {Text: "green"}}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{test, edit}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stalled || res.Repeats != 0 || test.invoked != 3 || res.Text != "green" {
		t.Fatalf("Stalled = %v, Repeats = %d, tests run = %d, Text = %q", res.Stalled, res.Repeats, test.invoked, res.Text)
	}
}

func TestRunNeverStallsWhenToldNotTo(t *testing.T) {
	tool := &fakeTool{name: "search", result: "nothing"}
	same := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "search", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{same, same, same}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxSteps: 3, StallAfter: -1})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stalled || !res.Truncated || tool.invoked != 3 {
		t.Fatalf("Stalled = %v, Truncated = %v, invoked = %d", res.Stalled, res.Truncated, tool.invoked)
	}
}
