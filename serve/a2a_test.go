package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternative/packages/go/cortex/agent"
)

// fakeModel answers every completion with answer, so a test exercises the
// protocol and the turn plumbing rather than a model.
func fakeModel(t *testing.T, answer string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, answer)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// a2aServer starts a cortex whose A2A surface is reachable, and returns the
// base URL to call it on.
func a2aServer(t *testing.T, answer string) string {
	t.Helper()
	isolateSkills(t)
	srv, err := New(Config{
		Root:     t.TempDir(),
		Token:    "secret",
		Provider: agent.Provider{Model: "test-model", BaseURL: fakeModel(t, answer), APIKey: "k"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go srv.Serve(ln)
	return "http://" + ln.Addr().String()
}

// callA2A sends one JSON-RPC request to the A2A endpoint.
func callA2A(t *testing.T, base, token, method string, params any) (map[string]any, int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+a2aPath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", a2aPath, err)
	}
	defer res.Body.Close()

	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode
}

func TestA2ASendMessageAnswers(t *testing.T) {
	const answer = "the workspace holds one file"
	base := a2aServer(t, answer)

	out, status := callA2A(t, base, "secret", "message/send", map[string]any{
		"message": map[string]any{
			"role":      "user",
			"messageId": a2a.NewMessageID(),
			"parts":     []map[string]any{{"kind": "text", "text": "what is here?"}},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("status is %d, want 200: %v", status, out)
	}
	if e, ok := out["error"]; ok {
		t.Fatalf("protocol error: %v", e)
	}
	if !bytes.Contains(mustJSON(t, out["result"]), []byte(answer)) {
		t.Errorf("answer %q missing from result: %s", answer, mustJSON(t, out["result"]))
	}
}

// The A2A surface runs the agent, so it sits behind the same token as every
// other call. Speaking a standard does not make an endpoint public.
func TestA2ARequiresTheToken(t *testing.T) {
	base := a2aServer(t, "never reached")

	_, status := callA2A(t, base, "", "message/send", map[string]any{
		"message": map[string]any{
			"role":      "user",
			"messageId": a2a.NewMessageID(),
			"parts":     []map[string]any{{"kind": "text", "text": "hello"}},
		},
	})
	if status != http.StatusUnauthorized {
		t.Errorf("status is %d, want 401", status)
	}
}

func TestA2AUnknownMethodIsARPCError(t *testing.T) {
	base := a2aServer(t, "unused")

	out, _ := callA2A(t, base, "secret", "tasks/nonesuch", map[string]any{})
	if _, ok := out["error"]; !ok {
		t.Errorf("unknown method accepted: %v", out)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// An A2A context is a session: a second task under the same contextId
// continues the history the first one built, which is what ADR 0009 means by
// a conversation being an A2A context.
func TestA2AContextCarriesTheConversation(t *testing.T) {
	base := a2aServer(t, "noted")

	send := func(contextID string) map[string]any {
		msg := map[string]any{
			"role":      "user",
			"messageId": a2a.NewMessageID(),
			"parts":     []map[string]any{{"kind": "text", "text": "remember this"}},
		}
		if contextID != "" {
			msg["contextId"] = contextID
		}
		out, status := callA2A(t, base, "secret", "message/send", map[string]any{"message": msg})
		if status != http.StatusOK {
			t.Fatalf("status is %d, want 200: %v", status, out)
		}
		return out
	}

	first := send("")
	ctxID := contextIDOf(t, first)
	if ctxID == "" {
		t.Fatalf("no contextId in first result: %s", mustJSON(t, first["result"]))
	}

	second := send(ctxID)
	if got := contextIDOf(t, second); got != ctxID {
		t.Errorf("second task ran under context %q, want %q", got, ctxID)
	}
}

func contextIDOf(t *testing.T, out map[string]any) string {
	t.Helper()
	result, ok := out["result"].(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := result["contextId"].(string); ok {
		return id
	}
	return ""
}
