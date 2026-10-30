package serve

import (
	"slices"
	"sync"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/tools"
)

var webTools = []string{"web_search", "fetch_url", "research", "explore_site"}

type researchSpend struct {
	mu    sync.Mutex
	usage agent.Usage
}

func (r *researchSpend) add(u agent.Usage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.usage.Add(u)
}

func (r *researchSpend) total() agent.Usage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

// turnTools is the server's tools plus, for a turn the person asked to
// research in depth and when it reads the web, deep_research over the turn's
// own client, whose spending lands in spent.
func (s *Server) turnTools(client agent.Client, asked bool, spent *researchSpend) []agent.Tool {
	if !asked {
		return s.tools
	}
	var web []agent.Tool
	for _, t := range s.tools {
		if slices.Contains(webTools, t.Name()) {
			web = append(web, t)
		}
	}
	if len(web) == 0 {
		return s.tools
	}
	return append(slices.Clone(s.tools), tools.NewDeepResearch(tools.DeepResearchConfig{
		Client:        client,
		Tools:         web,
		ContextWindow: s.cfg.ContextWindow,
		OnUsage:       spent.add,
	}))
}

// delegatedResearch keeps deep_research for an A2A caller: another agent
// that delegates a task has no person behind it to turn the switch on.
const delegatedResearch = true
