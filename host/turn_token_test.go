package host

import (
	"net/http"
	"sync"
	"testing"
)

type seenTokens struct {
	mu     sync.Mutex
	tokens []string
}

func (s *seenTokens) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.tokens = append(s.tokens, TurnToken(req.Context()))
	s.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func TestTheModelCallsOfATurnSeeTheTokenItsCallerLent(t *testing.T) {
	provider, _ := fakeModel(t, "")
	seen := &seenTokens{}
	provider.HTTPClient = &http.Client{Transport: seen}
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t"})

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-t1", map[string]any{TurnTokenKey: "lent-for-this-turn"})))
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("encore", "ctx-t2", nil)))

	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.tokens) != 2 || seen.tokens[0] != "lent-for-this-turn" || seen.tokens[1] != "" {
		t.Fatalf("model calls saw %q", seen.tokens)
	}
}
