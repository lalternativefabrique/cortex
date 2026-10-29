package serve

import (
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

var memoryTools = []string{"recall_memory", "recent_activity"}

// memoryTool reports a tool that reads what came before, offered in process
// or by an MCP server under a "<server>__" prefix.
func memoryTool(name string) bool {
	for _, tool := range memoryTools {
		if name == tool || strings.HasSuffix(name, "__"+tool) {
			return true
		}
	}
	return false
}

func withoutMemory(tools []agent.Tool, incognito bool) []agent.Tool {
	if !incognito {
		return tools
	}
	kept := make([]agent.Tool, 0, len(tools))
	for _, t := range tools {
		if !memoryTool(t.Name()) {
			kept = append(kept, t)
		}
	}
	return kept
}

func deferredWithoutMemory(sets []agent.Toolset, incognito bool) []agent.Toolset {
	if !incognito {
		return sets
	}
	kept := make([]agent.Toolset, 0, len(sets))
	for _, set := range sets {
		set.Tools = withoutMemory(set.Tools, true)
		if len(set.Tools) > 0 {
			kept = append(kept, set)
		}
	}
	return kept
}
