package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Toolset is a group of tools withheld from the model until it asks for
// them: a browser's two dozen schemas cost thousands of tokens on every step
// of every turn that never opens a page.
type Toolset struct {
	Name         string
	Summary      string
	Instructions string
	Tools        []Tool
}

// LoadToolsName is the tool through which the model asks for a Toolset.
const LoadToolsName = "load_tools"

type loadToolsArgs struct {
	Set string `json:"set" jsonschema:"description=Name of the tool set to load."`
}

type loadTools struct {
	sets []Toolset
}

func (t loadTools) Name() string { return LoadToolsName }

func (t loadTools) Description() string {
	var b strings.Builder
	b.WriteString("Load a set of tools that is available but not yet offered. Once loaded, its tools stay offered for the rest of the conversation. Load a set only when the task needs it.\n\nSets:")
	for _, s := range t.sets {
		fmt.Fprintf(&b, "\n- %s (%d tools)", s.Name, len(s.Tools))
		if s.Summary != "" {
			fmt.Fprintf(&b, ": %s", s.Summary)
		}
	}
	return b.String()
}

func (t loadTools) InputSchema() any { return loadToolsArgs{} }

func (t loadTools) Execute(_ context.Context, args json.RawMessage) (ToolResult, error) {
	var a loadToolsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return ToolResult{Content: "invalid arguments: " + err.Error()}, nil
	}
	i := slices.IndexFunc(t.sets, func(s Toolset) bool { return s.Name == a.Set })
	if i < 0 {
		return ToolResult{Content: fmt.Sprintf("no tool set named %q", a.Set)}, nil
	}
	names := make([]string, len(t.sets[i].Tools))
	for j, tool := range t.sets[i].Tools {
		names[j] = tool.Name()
	}
	loaded := fmt.Sprintf("Loaded %s: %s", a.Set, strings.Join(names, ", "))
	if t.sets[i].Instructions != "" {
		loaded += "\n\n" + t.sets[i].Instructions
	}
	return ToolResult{Content: loaded}, nil
}

// loadedSets reads which sets the history asked for. Deriving it from the
// conversation keeps a set loaded across turns without the host carrying
// state, and drops it with the call once compaction summarises it away.
func loadedSets(history []Message) map[string]bool {
	loaded := map[string]bool{}
	for _, m := range history {
		for _, c := range m.ToolCalls {
			if c.Name != LoadToolsName {
				continue
			}
			var a loadToolsArgs
			if json.Unmarshal(c.Arguments, &a) == nil {
				loaded[a.Set] = true
			}
		}
	}
	return loaded
}

func (r *Runner) offered(history []Message) ([]Tool, int) {
	if len(r.cfg.Deferred) == 0 {
		return r.cfg.Tools, r.toolTokens
	}
	loaded := loadedSets(history)
	tools := slices.Clone(r.cfg.Tools)
	tokens := r.toolTokens
	for i, s := range r.cfg.Deferred {
		if loaded[s.Name] {
			tools = append(tools, s.Tools...)
			tokens += r.deferredTokens[i]
		}
	}
	return tools, tokens
}
