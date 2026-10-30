package lalter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/recall"
)

type seen struct {
	method, path, auth, endUser string
	body                        map[string]any
}

func lalter(t *testing.T, status int, reply string) (*Store, *[]seen) {
	t.Helper()
	var calls []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := seen{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), endUser: r.Header.Get("X-End-User-Id")}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &c.body)
		calls = append(calls, c)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return New(Config{BaseURL: srv.URL + "/api/v1/", AppKey: "lalter_sk_x"}), &calls
}

var marie = recall.Scope{Subject: "marie-42", Agent: "cerveau"}

func TestRememberSendsTheEntryForItsSubject(t *testing.T) {
	store, calls := lalter(t, http.StatusNoContent, "")
	at := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	err := store.Remember(context.Background(), recall.Entry{Scope: marie, Conversation: "c1", Message: "m1", Kind: recall.Did, Role: "search_documents", Content: "3 extraits", At: at})
	if err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.method != http.MethodPost || c.path != "/api/v1/recall/entries" || c.auth != "Bearer lalter_sk_x" || c.endUser != "marie-42" {
		t.Fatalf("call = %+v", c)
	}
	if c.body["agent"] != "cerveau" || c.body["kind"] != "did" || c.body["at"] != "2026-09-03T10:00:00Z" || c.body["conversation"] != "c1" {
		t.Fatalf("body = %v", c.body)
	}
}

func TestRecallReturnsEntriesUnderTheAskedScope(t *testing.T) {
	store, calls := lalter(t, http.StatusOK, `{"entries":[{"conversation":"c1","message":"m1","kind":"said","role":"user","content":"budget 5000","at":"2026-09-03T10:00:00Z"}]}`)
	got, err := store.Recall(context.Background(), marie, "budget", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Scope != marie || got[0].Kind != recall.Said || got[0].Content != "budget 5000" || got[0].At.Day() != 3 {
		t.Fatalf("got %+v", got)
	}
	if (*calls)[0].body["query"] != "budget" || (*calls)[0].body["limit"] != float64(5) {
		t.Fatalf("body = %v", (*calls)[0].body)
	}
}

func TestForgetNamesTheConversation(t *testing.T) {
	store, calls := lalter(t, http.StatusNoContent, "")
	if err := store.Forget(context.Background(), marie, "task:9"); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[0]; c.method != http.MethodDelete || c.path != "/api/v1/recall/conversations/task:9" || c.endUser != "marie-42" {
		t.Fatalf("call = %+v", c)
	}
}

func TestARefusalCarriesLaltersAnswer(t *testing.T) {
	store, _ := lalter(t, http.StatusBadRequest, `{"error":"kind must be said, did or produced"}`)
	err := store.Remember(context.Background(), recall.Entry{Scope: marie, Kind: "dreamt", Content: "x"})
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "kind must be") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoSubjectNoCall(t *testing.T) {
	store, calls := lalter(t, http.StatusOK, `{}`)
	if _, err := store.Recall(context.Background(), recall.Scope{Agent: "cerveau"}, "x", 5); err == nil {
		t.Fatal("recalled without a subject")
	}
	if len(*calls) != 0 {
		t.Fatalf("lalter was called %d times", len(*calls))
	}
}
