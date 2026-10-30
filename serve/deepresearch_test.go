package serve

import (
	"context"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/tools"
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

func TestDeepResearchIsOfferedOnlyWhenThePersonAsks(t *testing.T) {
	offline, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if names(offline.turnTools(noModel{}, true, &researchSpend{}))["deep_research"] {
		t.Fatal("a server without the web has nothing to research with")
	}

	online, err := New(Config{Root: t.TempDir(), Searcher: noSearch{}})
	if err != nil {
		t.Fatal(err)
	}
	if names(online.turnTools(noModel{}, false, &researchSpend{}))["deep_research"] {
		t.Fatal("deep_research was offered on a turn the person did not ask for it")
	}
	if !names(online.turnTools(noModel{}, true, &researchSpend{}))["deep_research"] {
		t.Fatal("the person asked for deep research and it was not offered")
	}
	if names(online.tools)["deep_research"] {
		t.Fatal("deep_research is built per turn, over that turn's client")
	}
}
