package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

const toolName = "recall_memory"

// ResultLimit is how many memories one recall returns.
const ResultLimit = 5

type input struct {
	Query string `json:"query" jsonschema:"description=What to look for, as a natural-language question or phrase."`
}

type tool struct {
	store Store
	scope Scope
}

// NewTool returns recall_memory, searching store within scope only.
func NewTool(store Store, scope Scope) agent.Tool {
	return &tool{store: store, scope: scope}
}

func (t *tool) Name() string { return toolName }

func (t *tool) Description() string {
	return strings.Join([]string{
		"Search past conversations for a decision, constraint, or fact that was established there, and for what you already did or produced.",
		"",
		"Use it when this conversation references something that may have been settled or done earlier — a choice already made, a preference already stated, a reminder already set — rather than asking again, guessing, or doing it twice.",
	}, "\n")
}

func (t *tool) InputSchema() any { return input{} }

func (t *tool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return failure("could not parse arguments: %v", err), nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return failure("query is empty"), nil
	}
	found, err := t.store.Recall(ctx, t.scope, in.Query, ResultLimit)
	if err != nil {
		return failure("could not search memory: %v", err), nil
	}
	if len(found) == 0 {
		return agent.ToolResult{Content: "nothing found"}, nil
	}
	var b strings.Builder
	for _, e := range found {
		fmt.Fprintf(&b, "[%s] %s %s: %s\n", e.At.Format("2006-01-02"), e.Kind, e.Role, e.Content)
	}
	return agent.ToolResult{Content: b.String()}, nil
}

func failure(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + fmt.Sprintf(format, args...), Metadata: map[string]any{"ok": false}}
}
