package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/profile"
)

func TestATurnOnTheMachineReadsAndLearnsTheProfile(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		sse(w, "Voici le mail.")
	}))
	defer model.Close()

	store := profile.NewFileStoreAt(t.TempDir())
	if err := store.Assert(context.Background(), profile.Fact{ID: "f1", Facet: profile.Tone, Text: "tutoiement, phrases courtes", Source: profile.Said, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Root:         t.TempDir(),
		Token:        "t",
		Instructions: "You help a person with their day.",
		Profile:      store,
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
	s.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("model calls = %d: the answer, then the derivation", len(bodies))
	}
	if !strings.Contains(bodies[0], "tone: tutoiement, phrases courtes") || !strings.Contains(bodies[0], "You help a person") {
		t.Fatalf("the turn's prompt lacks the profile:\n%s", bodies[0])
	}
	if !strings.Contains(bodies[1], "update_profile") {
		t.Fatalf("no derivation:\n%s", bodies[1])
	}
	signals, _ := store.Signals(context.Background(), profile.Scope{}, time.Time{}, 10)
	if len(signals) != 1 || signals[0].Content != "écris le mail au banquier" {
		t.Fatalf("signals = %+v", signals)
	}
}
