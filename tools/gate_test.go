package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

type echoTool struct{ calls int }

func (e *echoTool) Name() string        { return "echo" }
func (e *echoTool) Description() string { return "echo" }
func (e *echoTool) InputSchema() any    { return struct{}{} }
func (e *echoTool) Execute(context.Context, json.RawMessage) (agent.ToolResult, error) {
	e.calls++
	return agent.ToolResult{Content: "ran"}, nil
}

type recordingApprover struct {
	decision Decision
	err      error
	got      Request
}

func (r *recordingApprover) Approve(_ context.Context, req Request) (Decision, error) {
	r.got = req
	return r.decision, r.err
}

func TestAGatedToolRunsOnlyWhenApproved(t *testing.T) {
	inner := &echoTool{}
	a := &recordingApprover{decision: Deny, err: RefusedByRule}
	g := Gate(inner, a, "mcp:https://x/mcp")

	res, err := g.Execute(context.Background(), json.RawMessage(`{"q":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if inner.calls != 0 || !strings.Contains(res.Content, string(RefusedByRule)) {
		t.Fatalf("calls %d, content %q: a refused call must not run", inner.calls, res.Content)
	}
	if a.got.Tool != "mcp:https://x/mcp" || a.got.Action != `{"q":1}` || a.got.Scope != "" {
		t.Fatalf("request = %+v", a.got)
	}

	a.decision, a.err = Allow, nil
	if res, _ := g.Execute(context.Background(), nil); res.Content != "ran" || g.Name() != "echo" {
		t.Fatalf("allowed call: content %q name %q", res.Content, g.Name())
	}
}
