package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/host"
)

// model answers the first request with a call to tool (or text when tool is
// empty) and every later one with text.
func model(t *testing.T, tool, args, text string) (agent.Provider, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		}
		if n == 0 && tool != "" {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": tool, "arguments": args},
			}}}, "finish_reason": "tool_calls"}}})
		} else {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": text}, "finish_reason": "stop"}}})
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", Model: "m"}, &bodies
}

func serve(t *testing.T, provider agent.Provider) Agent {
	t.Helper()
	h, err := host.New(context.Background(), host.Config{Agent: host.Agent{Name: "partage"}, Provider: provider, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return Agent{URL: srv.URL, Token: "tok"}
}

type style struct {
	Category string   `json:"category" jsonschema:"description=The kind of product the site reads as."`
	Energy   int      `json:"energy" jsonschema:"minimum=0,maximum=100"`
	Banned   []string `json:"banned_phrases"`
}

func TestAskReturnsTheAnswerAsTheTypeItAskedFor(t *testing.T) {
	provider, bodies := model(t, "respond", `{"category":"devtools","energy":70,"banned_phrases":["révolutionnaire"]}`, "")
	got, _, err := Ask[style](context.Background(), serve(t, provider), Request{Text: "suggest a style", Context: "You write for devtools."})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "devtools" || got.Energy != 70 || len(got.Banned) != 1 {
		t.Errorf("got %+v", got)
	}
	sent := (*bodies)[0]
	for _, want := range []string{`"banned_phrases"`, "The kind of product the site reads as.", "You write for devtools."} {
		if !strings.Contains(sent, want) {
			t.Errorf("the model was not sent %q: %s", want, sent)
		}
	}
}

func TestAskRefusesAnAnswerOutsideTheTypesBounds(t *testing.T) {
	provider, bodies := model(t, "respond", `{"category":"devtools","energy":140,"banned_phrases":[]}`, "no")
	_, _, err := Ask[style](context.Background(), serve(t, provider), Request{Text: "suggest a style"})
	if err == nil {
		t.Fatal("an energy of 140 was accepted")
	}
	if !strings.Contains((*bodies)[1], "does not match the expected format") {
		t.Errorf("the refusal was not sent back to the model: %s", (*bodies)[1])
	}
}

func TestSayReturnsTheTextAnswer(t *testing.T) {
	provider, _ := model(t, "", "", "Bonjour.")
	got, _, err := Say(context.Background(), serve(t, provider), Request{Text: "salut"})
	if err != nil || got != "Bonjour." {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestAWrongTokenIsAnError(t *testing.T) {
	provider, _ := model(t, "", "", "x")
	a := serve(t, provider)
	a.Token = "nope"
	if _, _, err := Say(context.Background(), a, Request{Text: "x"}); err == nil {
		t.Error("a refused call was not an error")
	}
}
