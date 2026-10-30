package serve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
)

type journalFunc func(context.Context, JournalEntry) error

func (f journalFunc) Record(ctx context.Context, e JournalEntry) error { return f(ctx, e) }

func readsThenAnswers(t *testing.T) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			sse(w, "Fait.")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"tool_calls":[`+
			`{"index":0,"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"notes.txt\"}"}},`+
			`{"index":1,"id":"c2","type":"function","function":{"name":"read","arguments":"{\"path\":\"../../etc/passwd\"}"}}`+
			`]},"finish_reason":"tool_calls"}]}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

func runTurn(t *testing.T, journal Journal, modelURL string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		Root:         root,
		Token:        "t",
		Instructions: "You help a person with their day.",
		Journal:      journal,
		Provider:     agent.Provider{BaseURL: modelURL + "/v1", APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	srv := httptest.NewServer(s.authenticated(mux))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/turn", strings.NewReader(`{"message":"lis mes notes"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return string(body)
}

func TestAFinishedTurnIsJournaledOnceWithItsActions(t *testing.T) {
	model := readsThenAnswers(t)
	defer model.Close()

	entries := make(chan JournalEntry, 4)
	runTurn(t, journalFunc(func(_ context.Context, e JournalEntry) error {
		entries <- e
		return nil
	}), model.URL)

	var e JournalEntry
	select {
	case e = <-entries:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn was never journaled")
	}
	select {
	case extra := <-entries:
		t.Fatalf("journaled twice: %+v", extra)
	case <-time.After(200 * time.Millisecond):
	}

	if e.Conversation == "" || e.Turn == "" {
		t.Fatalf("missing ids: %+v", e)
	}
	if e.Question != "lis mes notes" || e.Answer != "Fait." || e.TitleHint != "lis mes notes" || e.Err != "" {
		t.Fatalf("entry %+v", e)
	}
	if len(e.ToolCalls) != 2 {
		t.Fatalf("tool calls %+v", e.ToolCalls)
	}
	byTarget := map[string]JournalToolCall{}
	for _, c := range e.ToolCalls {
		byTarget[c.Target] = c
	}
	if c := byTarget["notes.txt"]; c.Tool != "read" || c.Outcome != OutcomeOK {
		t.Fatalf("notes.txt: %+v", c)
	}
	if c := byTarget["../../etc/passwd"]; c.Outcome != OutcomeError || c.Err == "" {
		t.Fatalf("outside root: %+v", c)
	}
}

func TestAFailingJournalDoesNotBreakTheTurn(t *testing.T) {
	model := readsThenAnswers(t)
	defer model.Close()

	called := make(chan struct{}, 1)
	body := runTurn(t, journalFunc(func(context.Context, JournalEntry) error {
		called <- struct{}{}
		return errors.New("journal down")
	}), model.URL)

	if !strings.Contains(body, `"Fait."`) || !strings.Contains(body, `"done"`) {
		t.Fatalf("turn did not finish: %s", body)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("journal never called")
	}
}

func TestATargetIsTheToolsMainArgument(t *testing.T) {
	cases := map[string]string{
		`{"command":"ls -la"}`:           "ls -la",
		`{"url":"https://a.b","x":"y"}`:  "https://a.b",
		`{"query":"météo"}`:              "météo",
		`{"zeta":"z","alpha":"a","n":1}`: "a",
		`not json`:                       "not json",
		`{}`:                             "",
	}
	for args, want := range cases {
		if got := toolTarget(args); got != want {
			t.Errorf("toolTarget(%s) = %q, want %q", args, got, want)
		}
	}
}

func TestATurnThatFailsIsStillJournaled(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadRequest)
	}))
	defer model.Close()

	entries := make(chan JournalEntry, 1)
	runTurn(t, journalFunc(func(_ context.Context, e JournalEntry) error {
		entries <- e
		return nil
	}), model.URL)

	select {
	case e := <-entries:
		if e.Err == "" || e.Answer != "" || e.Question != "lis mes notes" {
			t.Fatalf("entry %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a failed turn was never journaled")
	}
}
