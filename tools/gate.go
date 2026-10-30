package tools

import (
	"context"
	"encoding/json"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Gate asks a before every call of t, naming the call action. It is for the
// tools that do not gate themselves; bash, edit and write already ask with
// what they are about to run or change.
func Gate(t agent.Tool, a Approver, action string) agent.Tool {
	return &gated{Tool: t, approver: a, action: action}
}

type gated struct {
	agent.Tool
	approver Approver
	action   string
}

func (g *gated) Execute(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
	refused, err := approve(ctx, g.approver, Request{Tool: g.action, Action: string(args)})
	if err != nil {
		return agent.ToolResult{}, err
	}
	if refused != "" {
		return agent.ToolResult{
			Content:  "refused: " + refused,
			Metadata: map[string]any{"ok": false, "refused": true},
		}, nil
	}
	return g.Tool.Execute(ctx, args)
}
