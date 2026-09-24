package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/mcp"
	"github.com/lalternative/packages/go/cortex/recall"
)

// fakeModel streams a tool call on its first request and the answer in two
// chunks after, and keeps every request body it was sent.
func fakeModel(t *testing.T, tool string) (agent.Provider, *[]string) {
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
				"index": 0, "id": "c1", "type": "function",
				"function": map[string]any{"name": tool, "arguments": `{"q":"budget"}`},
			}}}, "finish_reason": "tool_calls"}}})
		} else {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": "5 000 "}}}})
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": "€ [1]."}, "finish_reason": "stop"}}})
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", Model: "m"}, &bodies
}

// fakeMCP offers one tool, search, and keeps the Authorization of each call.
func fakeMCP(t *testing.T) (string, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "search", "description": "Search the corpus.", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			mu.Lock()
			auths = append(auths, r.Header.Get("Authorization"))
			mu.Unlock()
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": `[{"index":1,"excerpt":"5 000 €"}]`}}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &auths
}

type memory struct {
	mu      sync.Mutex
	entries []recall.Entry
}

func (m *memory) Remember(_ context.Context, e recall.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}
func (m *memory) Recall(context.Context, recall.Scope, string, int) ([]recall.Entry, error) {
	return nil, nil
}
func (m *memory) Forget(context.Context, recall.Scope, string) error { return nil }

func start(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	h, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func rpc(t *testing.T, url, token, method string, message map[string]any) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"message": message}})
	req, _ := http.NewRequest(http.MethodPost, url+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func message(text, contextID string, meta map[string]any) map[string]any {
	return map[string]any{
		"role": "user", "messageId": "msg-" + text, "contextId": contextID,
		"parts": []map[string]any{{"kind": "text", "text": text}}, "metadata": meta,
	}
}

type event struct {
	Kind     string `json:"kind"`
	Append   bool   `json:"append"`
	Last     bool   `json:"lastChunk"`
	Final    bool   `json:"final"`
	Artifact struct {
		Name  string           `json:"name"`
		Parts []map[string]any `json:"parts"`
	} `json:"artifact"`
	Status struct {
		State string `json:"state"`
	} `json:"status"`
}

func readStream(t *testing.T, res *http.Response) []event {
	t.Helper()
	defer res.Body.Close()
	var out []event
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Result event `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("frame %q: %v", line, err)
		}
		out = append(out, frame.Result)
	}
	return out
}

func TestAStreamedTurnCarriesTheAnswerTheToolCallsAndLendsTheGrantToMCPOnly(t *testing.T) {
	provider, bodies := fakeModel(t, "corpus__search")
	mcpURL, auths := fakeMCP(t)
	mem := &memory{}
	srv := start(t, Config{
		Agent:    Agent{Name: "cerveau", Instructions: "Tu réponds depuis le corpus."},
		Provider: provider,
		MCP:      mcp.Config{Servers: []mcp.ServerConfig{{Name: "corpus", URL: mcpURL}}},
		Recall:   mem,
		Token:    "sidecar-token",
	})

	res := rpc(t, srv.URL, "sidecar-token", "message/stream", message("quel budget ?", "ctx-1", map[string]any{
		SubjectKey:    "marie",
		MCPHeadersKey: map[string]any{"Authorization": "Bearer grant-marie"},
	}))
	events := readStream(t, res)

	var answer strings.Builder
	var toolCalls, finals int
	for _, ev := range events {
		switch {
		case ev.Kind == "artifact-update" && ev.Artifact.Name == "answer":
			for _, p := range ev.Artifact.Parts {
				answer.WriteString(p["text"].(string))
			}
		case ev.Kind == "artifact-update" && ev.Artifact.Name == "tool-call":
			toolCalls++
			data := ev.Artifact.Parts[0]["data"].(map[string]any)
			if data["tool"] != "corpus__search" || !strings.Contains(data["result"].(string), "5 000 €") {
				t.Fatalf("tool-call artifact %v", data)
			}
		case ev.Kind == "status-update" && ev.Final:
			finals++
			if ev.Status.State != "completed" {
				t.Fatalf("final state %q", ev.Status.State)
			}
		}
	}
	if answer.String() != "5 000 € [1]." || toolCalls != 1 || finals != 1 {
		t.Fatalf("answer %q, tool calls %d, finals %d: %+v", answer.String(), toolCalls, finals, events)
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer grant-marie" {
		t.Fatalf("MCP saw Authorization %q, want the turn's grant", *auths)
	}
	for _, b := range *bodies {
		if strings.Contains(b, "grant-marie") {
			t.Fatal("the grant reached the model")
		}
	}
	var remembered []string
	for _, e := range mem.entries {
		remembered = append(remembered, string(e.Kind)+"/"+e.Role)
		if e.Subject != "marie" || e.Agent != "cerveau" || e.Conversation != "ctx-1" {
			t.Fatalf("memory scope %+v", e)
		}
	}
	if strings.Join(remembered, " ") != "said/user did/corpus__search said/assistant" {
		t.Fatalf("remembered %v", remembered)
	}
}

func TestTheNextTurnOfAContextRemembersTheFirst(t *testing.T) {
	provider, bodies := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t"})
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("premier", "ctx-9", nil)))
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("second", "ctx-9", nil)))
	last := (*bodies)[len(*bodies)-1]
	if !strings.Contains(last, "premier") || !strings.Contains(last, "5 000 € [1].") || !strings.Contains(last, "second") {
		t.Fatalf("second turn sent %s", last)
	}
}

func TestMessageSendReturnsTheWholeAnswer(t *testing.T) {
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t"})
	res := rpc(t, srv.URL, "t", "message/send", message("bonjour", "ctx-2", nil))
	defer res.Body.Close()
	var reply struct {
		Result struct {
			Status    struct{ State string }
			Artifacts []struct {
				Name  string
				Parts []struct{ Text string }
			}
		}
	}
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	parts := 0
	for _, a := range reply.Result.Artifacts {
		for _, p := range a.Parts {
			text.WriteString(p.Text)
			parts++
			if p.Text == "" {
				t.Fatal("an empty part closes the answer")
			}
		}
	}
	if reply.Result.Status.State != "completed" || text.String() != "5 000 € [1]." {
		t.Fatalf("state %q answer %q (%d parts)", reply.Result.Status.State, text.String(), parts)
	}
}

func TestOnlyTheTokenReachesTheAgentButAnyoneReadsItsCard(t *testing.T) {
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "cerveau", Description: "Le cerveau."}, Provider: provider, Token: "t", PublicURL: "http://localhost:7400"})
	for _, token := range []string{"", "wrong"} {
		res := rpc(t, srv.URL, token, "message/send", message("x", "c", nil))
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q -> %d, want 401", token, res.StatusCode)
		}
	}
	res, err := http.Get(srv.URL + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var card map[string]any
	_ = json.NewDecoder(res.Body).Decode(&card)
	if card["name"] != "cerveau" || card["url"] != "http://localhost:7400/a2a" || card["capabilities"].(map[string]any)["streaming"] != true {
		t.Fatalf("card %v", card)
	}
}

func TestAnEmptyTokenRefusesEveryone(t *testing.T) {
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider})
	res := rpc(t, srv.URL, "", "message/send", message("x", "c", nil))
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", res.StatusCode)
	}
}
