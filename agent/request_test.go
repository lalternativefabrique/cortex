package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestPersonRequestIsTheLastThingThePersonSaid(t *testing.T) {
	ctx := withPersonRequest(context.Background(), []Message{
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "ok"},
		{Role: RoleUser, Content: "now fix the test"},
		{Role: RoleTool, Content: "output"},
	})
	if got := PersonRequest(ctx); got != "now fix the test" {
		t.Fatalf("PersonRequest = %q", got)
	}
	setPersonRequest(ctx, "stop, revert instead")
	if got := PersonRequest(ctx); got != "stop, revert instead" {
		t.Fatalf("a message written mid-turn did not replace the request: %q", got)
	}
	if PersonRequest(context.Background()) != "" {
		t.Fatal("outside a turn the request must be empty")
	}
}

func TestTheModelNeverSeesWhatRedactMasks(t *testing.T) {
	leaky := &fakeLeakTool{}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "1", Name: "leak", Arguments: []byte(`{}`)}}},
		{Text: "done"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{leaky}, MaxSteps: 3, Redact: func(s string) (string, bool) {
		return strings.ReplaceAll(s, "s3cr3t-value", "[secret redacted]"), strings.Contains(s, "s3cr3t-value")
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range client.calls[1].Messages {
		if strings.Contains(m.Content, "s3cr3t-value") {
			t.Fatalf("the model was sent the secret: %q", m.Content)
		}
	}
}

type fakeLeakTool struct{}

func (fakeLeakTool) Name() string        { return "leak" }
func (fakeLeakTool) Description() string { return "leaks" }
func (fakeLeakTool) InputSchema() any    { return struct{}{} }
func (fakeLeakTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: "PASSWORD=s3cr3t-value", Metadata: map[string]any{"stdout": "PASSWORD=s3cr3t-value"}}, nil
}
