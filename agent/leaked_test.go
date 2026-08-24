package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLeakedToolCallRecognisesRealDialects(t *testing.T) {
	// Both of these were emitted by actual models against a server that did
	// not parse their dialect.
	cases := []string{
		`[TOOL_CALLS]{"content": "package store"}`,
		"I'll read it.\n<function=read>\n<parameter=path>main.go</parameter>\n</function>",
		"<tool_call>\n{\"name\": \"bash\"}\n</tool_call>",
	}
	for _, c := range cases {
		if !LeakedToolCall(c) {
			t.Errorf("missed a leaked call: %.60q", c)
		}
	}
}

func TestLeakedToolCallIgnoresProse(t *testing.T) {
	// A false positive costs a wasted step, so prose about tools must not
	// trigger it.
	cases := []string{
		"I used the read tool to open the file.",
		"The tool_call field was empty in the response.",
		"Use function calling to invoke a tool.",
		"The bash tool returned exit code 1.",
		"",
	}
	for _, c := range cases {
		if LeakedToolCall(c) {
			t.Errorf("false positive on: %q", c)
		}
	}
}

func TestRunRecoversFromALeakedToolCall(t *testing.T) {
	tool := &fakeTool{name: "read", result: "file contents"}
	client := &scriptedClient{responses: []CompletionResponse{
		{Text: "<function=read>\n<parameter=path>main.go</parameter>\n</function>"},
		{ToolCalls: []ToolCall{{ID: "c", Name: "read", Arguments: json.RawMessage(`{}`)}}},
		{Text: "the file defines main"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "read main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recoveries != 1 {
		t.Fatalf("Recoveries = %d, want 1", res.Recoveries)
	}
	if res.Text != "the file defines main" {
		t.Fatalf("Text = %q; the run should have continued past the leak", res.Text)
	}
	if tool.invoked != 1 {
		t.Fatal("the retried tool never ran")
	}
}

func TestRecoveryTellsTheModelWhatWentWrong(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{
		{Text: "[TOOL_CALLS][{\"name\":\"bash\"}]"},
		{Text: "done"},
	}}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}

	second := client.calls[1].Messages
	last := second[len(second)-1]
	if last.Role != RoleUser {
		t.Fatalf("the notice was sent as %q, want a user turn the model will answer", last.Role)
	}
	if !strings.Contains(last.Content, "structured tool call") {
		t.Fatalf("the notice does not say what was wrong: %q", last.Content)
	}
}

func TestRecoveryIsBounded(t *testing.T) {
	// A model that keeps leaking is not going to correct itself; the run
	// should end with what it said rather than loop.
	leak := CompletionResponse{Text: "<tool_call>{\"name\":\"bash\"}</tool_call>"}
	client := &scriptedClient{responses: []CompletionResponse{leak, leak, leak, leak, leak}}
	r, err := NewRunner(Config{Client: client, MaxSteps: 10})
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recoveries > maxLeakRecoveries {
		t.Fatalf("Recoveries = %d, want at most %d", res.Recoveries, maxLeakRecoveries)
	}
	if res.Text == "" {
		t.Fatal("the run ended with nothing; the last reply should still be returned")
	}
}

func TestOrdinaryFinalAnswerIsNotRetried(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{
		{Text: "I read the file and it defines main."},
	}}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recoveries != 0 {
		t.Fatal("a plain answer was mistaken for a leaked tool call")
	}
	if res.Steps != 1 {
		t.Fatalf("Steps = %d, want 1", res.Steps)
	}
}
