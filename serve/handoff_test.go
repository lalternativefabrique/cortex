package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

const fakeSections = "## 4. What is known of the code\nflags.go defines the flag.\n\n" +
	"## 5. Decisions\nKeep the old name as an alias.\n\n" +
	"## 6. What failed\nNone.\n\n" +
	"## 7. Verification state\ngo test ./... passes.\n\n" +
	"## 8. What remains\nNext: update the docs for the renamed flag.\n\n" +
	"## 9. Open questions\nNone.\n"

// handoffModel reads four files on each turn's first steps, then answers.
// It writes the handoff's sections when asked by the compactor, and keeps
// every request the loop sent.
type handoffModel struct {
	mu    sync.Mutex
	steps int
	loop  []string
}

func (m *handoffModel) startTurn() {
	m.mu.Lock()
	m.steps = 0
	m.mu.Unlock()
}

func (m *handoffModel) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.loop...)
}

func (m *handoffModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	if len(req.Messages) > 0 && req.Messages[0].Content == agent.HandoffInstruction {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, fakeSections)
		return
	}
	m.mu.Lock()
	m.loop = append(m.loop, string(body))
	m.steps++
	step := m.steps
	m.mu.Unlock()
	if step > 4 {
		sse(w, "Renamed.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, `data: {"choices":[{"delta":{"tool_calls":[`+
		`{"index":0,"id":"c%d","type":"function","function":{"name":"read","arguments":"{\"path\":\"f%d.txt\"}"}}`+
		`]},"finish_reason":"tool_calls"}]}`+"\n\n", step, step)
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func handoffServer(t *testing.T, model *handoffModel) (*Server, *httptest.Server) {
	t.Helper()
	isolateSkills(t)
	modelSrv := httptest.NewServer(model)
	t.Cleanup(modelSrv.Close)
	root := t.TempDir()
	for i := 1; i <= 4; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.txt", i)), []byte(strings.Repeat("content\n", 50)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{
		Root:          root,
		Token:         "t",
		ContextWindow: 500,
		Provider:      agent.Provider{BaseURL: modelSrv.URL + "/v1", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, srv
}

func call(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func conversationOf(t *testing.T, stream string) string {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		var e Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) == nil && e.Kind == "conversation" {
			return e.Text
		}
	}
	t.Fatalf("no conversation event in %q", stream)
	return ""
}

func TestACompactedHistorySurvivesTheNextTurn(t *testing.T) {
	model := &handoffModel{}
	s, srv := handoffServer(t, model)

	_, stream := call(t, http.MethodPost, srv.URL+"/turn", `{"message":"rename the flag"}`)
	id := conversationOf(t, stream)
	if !strings.Contains(stream, `"kind":"compact_end"`) || !strings.Contains(stream, `"handoff":"# Handoff`) {
		t.Fatalf("no compact_end carrying the handoff in %q", stream)
	}
	s.mu.Lock()
	_, compacted := agent.HandoffOf(s.talks[id].history)
	s.mu.Unlock()
	if !compacted {
		t.Fatal("the turn ended without the compaction it ran")
	}

	model.startTurn()
	call(t, http.MethodPost, srv.URL+"/turn", fmt.Sprintf(`{"conversation":%q,"message":"now the docs"}`, id))
	next := model.requests()[len(model.requests())-5]
	if !strings.Contains(next, "# Handoff") || !strings.Contains(next, "rename the flag") {
		t.Fatalf("the next turn started without the compacted history: %s", next)
	}
	if strings.Contains(next, "Noted. What would you like to do?") {
		t.Fatal("the next turn was sent the turns the compaction had replaced")
	}
}

func TestAHandoffIsTakenOverInANewConversation(t *testing.T) {
	model := &handoffModel{}
	_, srv := handoffServer(t, model)

	_, stream := call(t, http.MethodPost, srv.URL+"/turn", `{"message":"rename the flag"}`)
	first := conversationOf(t, stream)

	status, body := call(t, http.MethodGet, srv.URL+"/handoff?conversation="+first, "")
	if status != http.StatusOK {
		t.Fatalf("GET /handoff = %d: %s", status, body)
	}
	var got HandoffResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Handoff.Task != "rename the flag" || !strings.Contains(got.Markdown, "Next: update the docs for the renamed flag.") {
		t.Fatalf("handoff = %+v", got)
	}

	if status, _ := call(t, http.MethodPost, srv.URL+"/turn", fmt.Sprintf(`{"conversation":%q,"handoff":%q}`, first, got.Markdown)); status != http.StatusConflict {
		t.Fatalf("a handoff into a conversation already under way = %d, want 409", status)
	}

	model.startTurn()
	payload, _ := json.Marshal(TurnRequest{Handoff: got.Markdown})
	_, stream = call(t, http.MethodPost, srv.URL+"/turn", string(payload))
	if second := conversationOf(t, stream); second == first {
		t.Fatal("the handoff continued the conversation it came from")
	}
	if !strings.Contains(stream, "Renamed.") {
		t.Fatalf("the new conversation did not carry the work on: %s", stream)
	}
	opening := model.requests()[len(model.requests())-5]
	for _, want := range []string{"rename the flag", "Next: update the docs for the renamed flag.", handoffResume} {
		if !strings.Contains(opening, want) {
			t.Errorf("the new conversation's first request lacks %q", want)
		}
	}
}
