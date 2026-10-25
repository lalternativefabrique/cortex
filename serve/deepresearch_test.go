package serve

import (
	"context"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/tools"
)

type noSearch struct{}

func (noSearch) Search(context.Context, tools.SearchQuery) ([]tools.SearchResult, error) {
	return nil, nil
}

type noModel struct{}

func (noModel) Complete(context.Context, agent.CompletionRequest) (agent.CompletionResponse, error) {
	return agent.CompletionResponse{}, nil
}

func names(ts []agent.Tool) map[string]bool {
	out := map[string]bool{}
	for _, t := range ts {
		out[t.Name()] = true
	}
	return out
}

func TestAServerThatReadsTheWebOffersDeepResearch(t *testing.T) {
	offline, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if names(offline.turnTools(noModel{}, &researchSpend{}))["deep_research"] {
		t.Fatal("a server without the web has nothing to research with")
	}

	online, err := New(Config{Root: t.TempDir(), Searcher: noSearch{}})
	if err != nil {
		t.Fatal(err)
	}
	turn := online.turnTools(noModel{}, &researchSpend{})
	if !names(turn)["deep_research"] {
		t.Fatal("a server that searches the web offers deep_research by default")
	}
	if names(online.tools)["deep_research"] {
		t.Fatal("deep_research is built per turn, over that turn's client")
	}
}
