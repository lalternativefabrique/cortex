package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

func TestATurnStopsWhenItsCallerLeaves(t *testing.T) {
	modelLeft := make(chan struct{})
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": "début"}}}})
		_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(modelLeft)
		case <-time.After(10 * time.Second):
		}
	}))
	defer model.Close()
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: agent.Provider{BaseURL: model.URL + "/v1", Model: "m"}, Token: "t"})

	ctx, leave := context.WithCancel(context.Background())
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "message/stream", "params": map[string]any{"message": message("long", "ctx-l", nil)}})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/a2a", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), `"working"`) {
			break
		}
	}
	leave()
	res.Body.Close()

	select {
	case <-modelLeft:
	case <-time.After(3 * time.Second):
		t.Fatal("the model kept answering a caller that had left")
	}
}
