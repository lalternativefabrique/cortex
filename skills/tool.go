package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternativefabrique/cortex/agent"
)

// ToolName is the tool through which the model opens a skill.
const ToolName = "use_skill"

type useArgs struct {
	Name string `json:"name" jsonschema:"description=The skill to open, by name."`
}

type tool struct {
	set Set
}

// NewTool returns use_skill over set: the model sees every skill's name and
// description on every step, and reads one's instructions only when the
// task calls for it, so a hundred skills cost a hundred lines, not a
// hundred documents. Nil when set is empty.
func NewTool(set Set) agent.Tool {
	if len(set) == 0 {
		return nil
	}
	return &tool{set: set}
}

func (t *tool) Name() string { return ToolName }

func (t *tool) Description() string {
	var b strings.Builder
	b.WriteString("Open a skill: a method written for a kind of task, which you then follow for the rest of the conversation. Open one as soon as the task matches its description, before doing the work your own way.\n\nSkills:")
	for _, name := range t.set.Names() {
		s := t.set[name]
		fmt.Fprintf(&b, "\n- %s", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&b, ": %s", s.Description)
		}
	}
	return b.String()
}

func (t *tool) InputSchema() any { return useArgs{} }

func (t *tool) Execute(_ context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in useArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return failure("could not parse arguments: %v", err), nil
	}
	s, ok := t.set.Lookup(in.Name)
	if !ok {
		return failure("no skill named %q; the skills are: %s", in.Name, strings.Join(t.set.Names(), ", ")), nil
	}
	return agent.ToolResult{Content: s.Prompt("")}, nil
}

func failure(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + fmt.Sprintf(format, args...), Metadata: map[string]any{"ok": false}}
}
