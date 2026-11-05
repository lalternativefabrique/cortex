package serve

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

func TestATurnThinksAsHardAsThePersonAsks(t *testing.T) {
	var mu sync.Mutex
	var efforts []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ReasoningEffort string `json:"reasoning_effort"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		efforts = append(efforts, body.ReasoningEffort)
		mu.Unlock()
		sse(w, "Bonjour.")
	}))
	defer model.Close()

	s, err := New(Config{
		Root:     t.TempDir(),
		Token:    "t",
		Provider: agent.Provider{BaseURL: model.URL + "/v1", APIKey: "k", Model: "m", ReasoningEffort: "medium"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	turn := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/turn", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode
	}

	if code := turn(`{"message":"Salut","reasoning_effort":"high"}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if code := turn(`{"message":"Salut"}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if code := turn(`{"message":"Salut","reasoning_effort":"extreme"}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown effort was accepted with status %d", code)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(efforts) < 2 || efforts[0] != "high" || efforts[len(efforts)-1] != "medium" {
		t.Fatalf("model saw efforts %v, want high then the server's medium", efforts)
	}
}

func TestTheServerListsTheEffortsATurnMayAsk(t *testing.T) {
	s, err := New(Config{Root: t.TempDir(), Token: "t", Provider: agent.Provider{Model: "m", ReasoningEffort: "low"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /reasoning", s.handleReasoning)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/reasoning", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var got ReasoningLevels
	_ = json.NewDecoder(res.Body).Decode(&got)
	if got.Default != "low" || len(got.Options) != len(agent.ReasoningEfforts) {
		t.Fatalf("levels = %+v", got)
	}
}
