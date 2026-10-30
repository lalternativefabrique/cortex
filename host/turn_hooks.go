package host

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/tools"
)

// TurnInfo is who a turn runs for and what it was asked with, for the hooks
// that decide its tools.
type TurnInfo struct {
	TaskID    a2a.TaskID
	ContextID string
	// Subject is the person the turn acts for (SubjectKey).
	Subject string
	// Token is the Bearer the caller lent the turn (TurnTokenKey).
	Token string
	// Skill is the declared skill the turn runs, empty for none.
	Skill    string
	Metadata map[string]any
	Files    []File
}

// gateTools asks a before each call of ts. An MCP tool, named
// <server>__<tool>, is asked as mcp:<server>; any other by its name.
func gateTools(ts []agent.Tool, a tools.Approver, mcpTool bool) []agent.Tool {
	if a == nil {
		return ts
	}
	out := make([]agent.Tool, len(ts))
	for i, t := range ts {
		action := t.Name()
		if mcpTool {
			server, _, _ := strings.Cut(t.Name(), "__")
			action = "mcp:" + server
		}
		out[i] = tools.Gate(t, a, action)
	}
	return out
}

// turnTools are the tools one turn is offered before memory and respond:
// the host's, the turn's own and the MCP servers', gated by the turn's
// approver, and describe_image when the message carries an image.
func (h *Host) turnTools(ctx context.Context, info TurnInfo) ([]agent.Tool, error) {
	own := append([]agent.Tool(nil), h.cfg.Tools...)
	if h.cfg.TurnTools != nil {
		extra, err := h.cfg.TurnTools(ctx, info)
		if err != nil {
			return nil, fmt.Errorf("turn tools: %w", err)
		}
		own = append(own, extra...)
	}
	var approver tools.Approver
	if h.cfg.Approver != nil {
		a, err := h.cfg.Approver(ctx, info)
		if err != nil {
			return nil, fmt.Errorf("turn approver: %w", err)
		}
		approver = a
	}
	out := append(gateTools(own, approver, false), gateTools(h.servers.forTurn(), approver, true)...)
	if t := newDescribeAttachedImage(h.cfg.Vision, info.Files); t != nil {
		out = append(out, t)
	}
	return out, nil
}
