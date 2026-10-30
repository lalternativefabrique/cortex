package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/host"
)

func fakeLentToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, _ := json.Marshal(claims)
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"EdDSA"}`)) + "." + enc(payload) + "." + enc([]byte("sig"))
}

func TestARecordLoggedUnderATurnNamesIt(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(turnHandler{slog.NewJSONHandler(&out, nil)}).With("agent", "cerveau")
	ctx := host.WithTurnToken(context.Background(), fakeLentToken(t, map[string]any{"jti": "turn-42", "sub": "key-synthiz"}))
	log.InfoContext(ctx, "cortex: task started")
	log.Info("cortex listening")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %q", out.String())
	}
	var first, second map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	_ = json.Unmarshal([]byte(lines[1]), &second)
	if first["turn_id"] != "turn-42" || first["agent"] != "cerveau" {
		t.Errorf("under a turn: %v", first)
	}
	if _, named := second["turn_id"]; named {
		t.Errorf("outside a turn nothing is named: %v", second)
	}
}

func TestATokenThatIsNotOneNamesNoTurn(t *testing.T) {
	for _, raw := range []string{"", "opaque-bearer", "a.b", "a.!!!.c", fakeLentToken(t, map[string]any{"sub": "k"})} {
		if id := turnID(raw); id != "" {
			t.Errorf("%q named %q", raw, id)
		}
	}
}

func TestWithoutSkalpaiTheContainerLogsToStdoutOnly(t *testing.T) {
	t.Setenv("SKALPAI_ENDPOINT", "")
	t.Setenv("SKALPAI_API_KEY", "")
	shutdown, err := telemetry(context.Background(), "cerveau")
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := slog.Default().Handler().(turnHandler); !ok {
		t.Errorf("the default logger must still name turns, got %T", slog.Default().Handler())
	}
}
