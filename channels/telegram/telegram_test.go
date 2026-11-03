package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI serves getUpdates once with the given updates, then nothing, and
// records every sendMessage.
type fakeAPI struct {
	mu      sync.Mutex
	updates []map[string]any
	served  bool
	sent    []map[string]any
	calls   []string
}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		var params map[string]any
		_ = json.NewDecoder(r.Body).Decode(&params)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, method)
		switch method {
		case "getUpdates":
			var result any = []any{}
			if !f.served {
				f.served = true
				result = f.updates
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
		case "sendMessage":
			f.sent = append(f.sent, params)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	}
}

func msg(id, user, chat, thread int64, text string) map[string]any {
	m := map[string]any{"message_id": id, "text": text, "from": map[string]any{"id": user}, "chat": map[string]any{"id": chat}}
	if thread != 0 {
		m["message_thread_id"] = thread
	}
	return map[string]any{"update_id": id, "message": m}
}

func serveFor(t *testing.T, api *fakeAPI, cfg Config, d time.Duration) *Bot {
	t.Helper()
	srv := httptest.NewServer(api.handler(t))
	t.Cleanup(srv.Close)
	cfg.Token = "t"
	cfg.BaseURL = srv.URL
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_ = b.Serve(ctx)
	time.Sleep(50 * time.Millisecond)
	return b
}

func TestTheBotAnswersAllowedUsersInTheirConversationOnly(t *testing.T) {
	api := &fakeAPI{updates: []map[string]any{
		msg(1, 42, 420, 0, "où en est le rapport ?"),
		msg(2, 99, 990, 0, "salut"),
		msg(3, 42, 420, 7, "et dans ce sujet ?"),
	}}
	var mu sync.Mutex
	var asked []string
	b := serveFor(t, api, Config{
		Allowed: []int64{42},
		Subject: func(id int64) string { return "owner" },
		Ask: func(_ context.Context, subject, conversation, text string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, subject+"|"+conversation+"|"+text)
			return "Réponse à: " + text, nil
		},
	}, 300*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 2 {
		t.Fatalf("asked = %v: the user not allowed must be ignored", asked)
	}
	joined := strings.Join(asked, "\n")
	if !strings.Contains(joined, "owner|telegram:420|où en est") || !strings.Contains(joined, "owner|telegram:420:7|et dans ce sujet") {
		t.Fatalf("asked = %v", asked)
	}
	api.mu.Lock()
	sent := append([]map[string]any(nil), api.sent...)
	api.mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("sent = %v", sent)
	}
	for _, s := range sent {
		if s["chat_id"].(float64) != 420 {
			t.Fatalf("answered the wrong chat: %v", s)
		}
		if strings.Contains(s["text"].(string), "ce sujet") && s["message_thread_id"].(float64) != 7 {
			t.Fatalf("the topic's answer left the topic: %v", s)
		}
	}
	if err := b.Deliver(context.Background(), "lundi: 2 pages à corriger"); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	last := api.sent[len(api.sent)-1]
	api.mu.Unlock()
	if last["chat_id"].(float64) != 420 || last["text"] != "lundi: 2 pages à corriger" {
		t.Fatalf("delivered %v", last)
	}
}

func TestNoAllowedUserMeansNoAnswerAndNoDelivery(t *testing.T) {
	api := &fakeAPI{updates: []map[string]any{msg(1, 42, 420, 0, "salut")}}
	called := false
	b := serveFor(t, api, Config{Ask: func(context.Context, string, string, string) (string, error) {
		called = true
		return "", nil
	}}, 200*time.Millisecond)
	if called {
		t.Fatal("a bot with no allowed user answered someone")
	}
	if err := b.Deliver(context.Background(), "x"); err == nil {
		t.Fatal("delivery with no known chat must fail loudly")
	}
}

func TestLongAnswersAreSplitOnLines(t *testing.T) {
	text := strings.Repeat("une ligne de réponse assez longue pour compter\n", 200)
	chunks := split(text, MaxMessageBytes)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for _, c := range chunks {
		if len(c) > MaxMessageBytes || strings.HasPrefix(c, "\n") || !strings.HasSuffix(c, "compter") && c != chunks[len(chunks)-1] {
			t.Fatalf("chunk %q", c[:min(len(c), 60)])
		}
	}
	if strings.Join(chunks, "\n") != strings.TrimRight(text, "\n") {
		t.Fatal("splitting lost text")
	}
	if got := split(strings.Repeat("é", 2001), 4000); len(got) != 2 || !strings.HasPrefix(got[1], "é") {
		t.Fatalf("a rune was cut: %d chunks", len(got))
	}
	if _, err := New(Config{Token: "t"}); err == nil {
		t.Fatal("a bot without Ask was accepted")
	}
}
