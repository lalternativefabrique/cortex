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

func TestATurnRunsOnTheModelThePersonPicked(t *testing.T) {
	var mu sync.Mutex
	var models []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		models = append(models, body.Model)
		mu.Unlock()
		sse(w, "Bonjour.")
	}))
	defer upstream.Close()

	s, err := New(Config{
		Root:     t.TempDir(),
		Token:    "t",
		Provider: agent.Provider{BaseURL: upstream.URL + "/v1", APIKey: "k", Model: "server-default"},
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
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
	}

	turn(`{"message":"Salut","model":"glm-5.3"}`)
	turn(`{"message":"Salut"}`)

	mu.Lock()
	defer mu.Unlock()
	if len(models) < 2 || models[0] != "glm-5.3" || models[len(models)-1] != "server-default" {
		t.Fatalf("provider saw models %v, want glm-5.3 then the server's own", models)
	}
}
