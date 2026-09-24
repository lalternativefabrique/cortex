package host

import (
	"strings"
	"testing"
)

func TestTwoPeopleSharingAContextIDDoNotShareItsHistory(t *testing.T) {
	provider, bodies := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "a"}, Provider: provider, Token: "t"})

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("le budget secret de marie", "ctx-same", map[string]any{SubjectKey: "marie"})))
	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-same", map[string]any{SubjectKey: "paul"})))

	if last := (*bodies)[len(*bodies)-1]; strings.Contains(last, "secret de marie") {
		t.Fatalf("paul's turn carried marie's history: %s", last)
	}
}
