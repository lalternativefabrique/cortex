package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

type namedTool string

func (n namedTool) Name() string        { return string(n) }
func (n namedTool) Description() string { return "test tool" }
func (n namedTool) InputSchema() any    { return map[string]any{"type": "object"} }
func (n namedTool) Execute(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

func TestAnIncognitoConversationNeitherRecallsNorRemembers(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(body))
		mu.Unlock()
		sse(w, "Bonjour.")
	}))
	defer model.Close()

	kept := &keptMemories{}
	journaled := make(chan JournalEntry, 4)
	s, err := New(Config{
		Root:         t.TempDir(),
		Token:        "t",
		Instructions: "You help a person with their day.",
		Recall:       kept,
		Journal: journalFunc(func(_ context.Context, e JournalEntry) error {
			journaled <- e
			return nil
		}),
		Tools:    []agent.Tool{namedTool("lalter__recall_memory"), namedTool("lalter__recent_activity"), namedTool("find_notes")},
		Provider: agent.Provider{BaseURL: model.URL + "/v1", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	turn := func(body string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/turn", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(res.Body)
		_ = res.Body.Close()
	}
	turn(`{"message":"salut","recall":false}`)
	var id string
	for k := range s.talks {
		id = k
	}
	turn(`{"conversation":"` + id + `","message":"encore","recall":true}`)

	select {
	case e := <-journaled:
		t.Fatalf("journaled %+v", e)
	case <-time.After(200 * time.Millisecond):
	}
	kept.mu.Lock()
	if len(kept.entries) > 0 {
		t.Fatalf("remembered %+v", kept.entries)
	}
	kept.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("model called %d times", len(sent))
	}
	for _, body := range sent {
		if strings.Contains(body, "recall_memory") || strings.Contains(body, "recent_activity") {
			t.Fatal("a tool reading the past was offered")
		}
		if !strings.Contains(body, "find_notes") {
			t.Fatal("the person's other tools were dropped")
		}
	}
}

func TestATurnCutOnItsStepsStillAnswers(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"tools"`) {
			sse(w, "Rien trouvé dans tes notes.")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[`+
			`{"index":0,"id":"c1","type":"function","function":{"name":"find_notes","arguments":"{}"}}`+
			`]},"finish_reason":"tool_calls"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer model.Close()

	s, err := New(Config{
		Root:         t.TempDir(),
		Token:        "t",
		Instructions: "You help a person with their day.",
		Tools:        []agent.Tool{namedTool("find_notes")},
		MaxSteps:     1,
		Provider:     agent.Provider{BaseURL: model.URL + "/v1", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/turn", strings.NewReader(`{"message":"retrouve le post"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !strings.Contains(string(body), "Rien trouvé dans tes notes.") {
		t.Fatalf("the cut turn ended without an answer: %s", body)
	}
}
