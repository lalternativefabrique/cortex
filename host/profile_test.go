package host

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/profile"
)

func TestATurnReadsTheProfileObservesWhatWasSaidAndLearnsAfter(t *testing.T) {
	ctx := context.Background()
	store := profile.NewFileStoreAt(t.TempDir())
	marie := profile.Scope{Subject: "marie"}
	if err := store.Assert(ctx, profile.Fact{ID: "f1", Scope: marie, Facet: profile.Tone, Text: "tutoiement, phrases courtes", Source: profile.Said, Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	provider, bodies := scriptedModel(t, scriptedReply{text: "Salut."})
	h, err := New(ctx, Config{Agent: Agent{Name: "ego", Instructions: "Tu aides."}, Provider: provider, Profile: store, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("écris le mail au banquier", "ctx-1", map[string]any{SubjectKey: "marie"})))
	h.Close()

	if len(*bodies) != 2 {
		t.Fatalf("model calls = %d: the answer, then the derivation", len(*bodies))
	}
	if first := (*bodies)[0]; !strings.Contains(first, "tone: tutoiement, phrases courtes") || !strings.Contains(first, "Tu aides.") {
		t.Fatalf("the turn's system prompt lacks the profile:\n%s", first)
	}
	if second := (*bodies)[1]; !strings.Contains(second, "update_profile") || !strings.Contains(second, "écris le mail au banquier") {
		t.Fatalf("the derivation lacks the tool or the signal:\n%s", second)
	}
	signals, err := store.Signals(ctx, marie, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 1 || signals[0].Kind != profile.SignalSaid || signals[0].Agent != "ego" || signals[0].Conversation != "ctx-1" {
		t.Fatalf("signals = %+v", signals)
	}
}

func TestATurnWithoutASubjectLeavesTheProfileAlone(t *testing.T) {
	store := profile.NewFileStoreAt(t.TempDir())
	provider, bodies := scriptedModel(t, scriptedReply{text: "Salut."})
	h, err := New(context.Background(), Config{Agent: Agent{Name: "ego"}, Provider: provider, Profile: store, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-2", nil)))
	h.Close()

	if len(*bodies) != 1 {
		t.Fatalf("model calls = %d: no derivation without a subject", len(*bodies))
	}
}
