package host

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

type lookup struct{ calls atomic.Int32 }

func (l *lookup) Name() string        { return "web_search" }
func (l *lookup) Description() string { return "Search the web.\nMore." }
func (l *lookup) InputSchema() any    { return map[string]any{"type": "object"} }
func (l *lookup) Execute(context.Context, json.RawMessage) (agent.ToolResult, error) {
	l.calls.Add(1)
	return agent.ToolResult{Content: `[{"title":"budget"}]`}, nil
}

func TestTheHostsOwnToolsAreCalledOnATurnWithoutAnyMCPServer(t *testing.T) {
	provider, bodies := fakeModel(t, "web_search")
	tool := &lookup{}
	srv := start(t, Config{Agent: Agent{Name: "partage"}, Provider: provider, Tools: []agent.Tool{tool}, Token: "t"})

	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("quel budget ?", "ctx-own", nil)))

	if tool.calls.Load() != 1 {
		t.Fatalf("the host's own tool was called %d times", tool.calls.Load())
	}
	if !strings.Contains((*bodies)[0], `"web_search"`) {
		t.Errorf("the model was not offered the host's tool: %s", (*bodies)[0])
	}
	var traced bool
	for _, ev := range events {
		if ev.Kind == "artifact-update" && ev.Artifact.Name == "tool-call" {
			traced = ev.Artifact.Parts[0]["data"].(map[string]any)["tool"] == "web_search"
		}
	}
	if !traced {
		t.Errorf("the call was not reported as a tool-call artifact: %+v", events)
	}
}

func TestTheCardListsTheHostsOwnTools(t *testing.T) {
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "partage"}, Provider: provider, Tools: []agent.Tool{&lookup{}}, Token: "t"})
	res, err := http.Get(srv.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var card struct {
		Skills []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"skills"`
	}
	_ = json.NewDecoder(res.Body).Decode(&card)
	if len(card.Skills) != 1 || card.Skills[0].ID != "web_search" || card.Skills[0].Description != "Search the web." {
		t.Errorf("skills = %+v", card.Skills)
	}
}
