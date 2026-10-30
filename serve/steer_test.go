package serve

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
)

func sse(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"`+text+`"}}]}`+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func TestAPrecisionSentDuringATurnReachesTheModelBeforeItEnds(t *testing.T) {
	firstCall := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var secondSaw atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch calls.Add(1) {
		case 1:
			close(firstCall)
			<-release
			sse(w, "first draft")
		default:
			secondSaw.Store(strings.Contains(string(body), "in French please"))
			sse(w, "réponse")
		}
	}))
	defer model.Close()

	s, err := New(Config{Root: t.TempDir(), Token: "t", Provider: agent.Provider{BaseURL: model.URL + "/v1", APIKey: "k", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	mux.HandleFunc("POST /turn/steer", s.handleSteer)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	post := func(path, body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := post("/turn/steer", `{"conversation":"none","message":"x"}`); res.StatusCode != http.StatusConflict {
		t.Fatalf("steering no running turn = %d, want 409", res.StatusCode)
	}

	stream := make(chan string, 1)
	go func() {
		res := post("/turn", `{"message":"explain"}`)
		b, _ := io.ReadAll(res.Body)
		stream <- string(b)
	}()
	select {
	case <-firstCall:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never reached the model")
	}
	s.mu.Lock()
	var id string
	for k, v := range s.talks {
		if v.running {
			id = k
		}
	}
	s.mu.Unlock()
	if res := post("/turn/steer", `{"conversation":"`+id+`","message":"in French please"}`); res.StatusCode != http.StatusAccepted {
		t.Fatalf("steer = %d", res.StatusCode)
	}
	close(release)

	got := <-stream
	if !secondSaw.Load() || calls.Load() != 2 {
		t.Fatalf("the model did not get the precision (calls %d)", calls.Load())
	}
	if !strings.Contains(got, `"kind":"steered"`) || !strings.Contains(got, "réponse") {
		t.Fatalf("stream = %s", got)
	}
}
