package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/host"
)

func streamingModel(t *testing.T, hits *atomic.Int32, bodies *[]string, mu *sync.Mutex) agent.Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, string(raw))
		mu.Unlock()
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, part := range []string{"bon", "jour"} {
			b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": part}}}})
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		}
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", Model: "m"}
}

var sharedHistory = true

func clusteredHost(t *testing.T, url string, provider agent.Provider, serve bool) (*httptest.Server, func()) {
	t.Helper()
	nc := connect(t, url)
	turns, err := NewTurns(context.Background(), nc, Config{Agent: "cerveau"})
	if err != nil {
		t.Fatal(err)
	}
	js, _ := jetstream.New(nc)
	history, err := NewHistory(context.Background(), js, "cerveau", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := host.Config{Agent: host.Agent{Name: "cerveau"}, Provider: provider, Token: "t", Turns: turns}
	if sharedHistory {
		cfg.History = history
	}
	h, err := host.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	stop := func() {}
	if serve {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = h.Serve(ctx); close(done) }()
		var once sync.Once
		stop = func() { once.Do(func() { cancel(); <-done }) }
		t.Cleanup(stop)
	}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return srv, stop
}

func ask(t *testing.T, base, text, contextID string) (answer, state string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "message/stream", "params": map[string]any{"message": map[string]any{
		"role": "user", "messageId": "m-" + text, "contextId": contextID,
		"parts": []map[string]any{{"kind": "text", "text": text}}, "metadata": map[string]any{host.SubjectKey: "p1"},
	}}})
	req, _ := http.NewRequest(http.MethodPost, base+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Result struct {
				Kind     string `json:"kind"`
				Final    bool   `json:"final"`
				Artifact struct {
					Name  string `json:"name"`
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"artifact"`
				Status struct {
					State string `json:"state"`
				} `json:"status"`
			} `json:"result"`
		}
		_ = json.Unmarshal([]byte(line), &frame)
		if frame.Result.Artifact.Name == "answer" {
			for _, p := range frame.Result.Artifact.Parts {
				answer += p.Text
			}
		}
		if frame.Result.Final {
			state = frame.Result.Status.State
		}
	}
	return answer, state
}

func TestAnA2ATurnReceivedByOneHostIsAnsweredByAnotherWithTheSharedConversation(t *testing.T) {
	url := broker(t)
	var hits atomic.Int32
	var bodies []string
	var mu sync.Mutex
	provider := streamingModel(t, &hits, &bodies, &mu)
	front, _ := clusteredHost(t, url, agent.Provider{BaseURL: "http://127.0.0.1:1/v1", Model: "never"}, false)
	_, stopFirst := clusteredHost(t, url, provider, true)

	answer, state := ask(t, front.URL, "premier", "ctx-1")
	if answer != "bonjour" || state != "completed" {
		t.Fatalf("first turn: %q %q", answer, state)
	}
	stopFirst()
	clusteredHost(t, url, provider, true)
	answer, state = ask(t, front.URL, "second", "ctx-1")
	if answer != "bonjour" || state != "completed" {
		t.Fatalf("second turn: %q %q", answer, state)
	}
	mu.Lock()
	defer mu.Unlock()
	last := bodies[len(bodies)-1]
	if !strings.Contains(last, "premier") || !strings.Contains(last, "second") {
		t.Fatalf("second turn did not carry the conversation: %s", last)
	}
}
