package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/host"
	"github.com/lalternativefabrique/cortex/profile"
)

func quietModel(t *testing.T) agent.Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"nothing to learn"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m"}
}

func serveAgent(t *testing.T, store profile.Store) Agent {
	t.Helper()
	h, err := host.New(context.Background(), host.Config{Agent: host.Agent{Name: "partage"}, Provider: quietModel(t), Token: "t", Profile: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return Agent{URL: srv.URL, Token: "t"}
}

func TestAnAppReportsGesturesReadsAndCorrectsTheProfile(t *testing.T) {
	store := profile.NewFileStoreAt(t.TempDir())
	a := serveAgent(t, store)
	ctx := context.Background()
	person := profile.Scope{Subject: "ana"}

	err := Observe(ctx, a, "ana", []Signal{
		{Kind: "edited", Target: "post 12", Content: "« Nous sommes ravis » → « On lance »"},
		{Kind: "pass", Weight: -1, Target: "post 40"},
	})
	if err != nil {
		t.Fatal(err)
	}
	signals, err := store.Signals(ctx, person, time.Time{}, 10)
	if err != nil || len(signals) != 2 {
		t.Fatalf("signals = %+v, %v", signals, err)
	}
	for _, s := range signals {
		if s.Agent != "partage" {
			t.Fatalf("signal %+v not attributed to the agent", s)
		}
	}

	if err := store.Assert(ctx, profile.Fact{ID: "f1", Scope: person, Facet: profile.Facet("tone"), Text: "writes short and direct", Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadProfile(ctx, a, "ana")
	if err != nil || len(got.Facts) != 1 || got.Facts[0].Text != "writes short and direct" {
		t.Fatalf("profile = %+v, %v", got, err)
	}
	if err := Retract(ctx, a, "ana", "f1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadProfile(ctx, a, "ana"); len(got.Facts) != 0 {
		t.Fatalf("retracted fact still served: %+v", got)
	}
}

func TestTheProfileRoutesRefuseAStrangerAndAnAgentWithoutProfile(t *testing.T) {
	ctx := context.Background()
	a := serveAgent(t, profile.NewFileStoreAt(t.TempDir()))
	a.Token = "wrong"
	if err := Observe(ctx, a, "ana", []Signal{{Kind: "said", Content: "x"}}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("stranger: %v", err)
	}
	none := serveAgent(t, nil)
	if _, err := ReadProfile(ctx, none, "ana"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("no profile: %v", err)
	}
	ok := serveAgent(t, profile.NewFileStoreAt(t.TempDir()))
	if err := Observe(ctx, ok, "", []Signal{{Kind: "said"}}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("no subject: %v", err)
	}
}
