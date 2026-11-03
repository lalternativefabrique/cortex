package host

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

func TestATurnFallsBackToTheNextModelWhenTheFirstRefuses(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model is gone"}`, http.StatusBadRequest)
	}))
	defer down.Close()
	provider, bodies := scriptedModel(t, scriptedReply{text: "Salut, depuis le secours."})
	srv := start(t, Config{
		Agent:     Agent{Name: "ego"},
		Provider:  agent.Provider{BaseURL: down.URL + "/v1", Model: "gone", APIKey: "k"},
		Fallbacks: []agent.Provider{provider},
		Token:     "t",
	})

	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-fb", nil)))
	var answer strings.Builder
	for _, ev := range events {
		if ev.Artifact.Name != "answer" {
			continue
		}
		for _, p := range ev.Artifact.Parts {
			if text, _ := p["text"].(string); text != "" {
				answer.WriteString(text)
			}
		}
	}
	if !strings.Contains(answer.String(), "depuis le secours") {
		t.Fatalf("answer = %q, events = %+v", answer.String(), events)
	}
	if len(*bodies) != 1 || !strings.Contains((*bodies)[0], `"model":"m"`) {
		t.Fatalf("the fallback was asked %d times with its own model: %v", len(*bodies), *bodies)
	}
}
