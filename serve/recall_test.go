package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/recall"
)

type keptMemories struct {
	mu      sync.Mutex
	entries []recall.Entry
}

func (k *keptMemories) Remember(_ context.Context, e recall.Entry) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.entries = append(k.entries, e)
	return nil
}

func (k *keptMemories) Recall(context.Context, recall.Scope, string, int) ([]recall.Entry, error) {
	return nil, nil
}

func (k *keptMemories) Forget(context.Context, recall.Scope, string) error { return nil }

func TestATurnIsRememberedWhereTheHostKeepsMemory(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sse(w, "Voici le mail.")
	}))
	defer model.Close()

	kept := &keptMemories{}
	s, err := New(Config{
		Root:         t.TempDir(),
		Token:        "t",
		Instructions: "You help a person with their day.",
		Recall:       kept,
		Provider:     agent.Provider{BaseURL: model.URL + "/v1", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/turn", strings.NewReader(`{"message":"écris le mail au banquier"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()

	kept.mu.Lock()
	defer kept.mu.Unlock()
	var said []string
	for _, e := range kept.entries {
		if e.Conversation == "" {
			t.Fatalf("a memory without its conversation: %+v", e)
		}
		said = append(said, e.Role+": "+e.Content)
	}
	if strings.Join(said, " | ") != "user: écris le mail au banquier | assistant: Voici le mail." {
		t.Fatalf("remembered %v", said)
	}
}
