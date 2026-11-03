package host

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/profile"
)

func TestASaidSignalIsBoundedWhateverTheMessageSize(t *testing.T) {
	store := profile.NewFileStoreAt(t.TempDir())
	provider, _ := scriptedModel(t, scriptedReply{text: "Salut."})
	h, err := New(context.Background(), Config{Agent: Agent{Name: "ego"}, Provider: provider, Profile: store, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	huge := strings.Repeat("é", 200_000)
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message(huge, "ctx-3", map[string]any{SubjectKey: "marie"})))
	h.Close()

	signals, _ := store.Signals(context.Background(), profile.Scope{Subject: "marie"}, time.Time{}, 10)
	if len(signals) != 1 || len(signals[0].Content) > profile.MaxSignalBytes {
		t.Fatalf("signals = %d, content bytes = %d", len(signals), len(signals[0].Content))
	}
}
