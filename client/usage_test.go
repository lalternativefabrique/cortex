package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func door(t *testing.T, reply string) Agent {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return Agent{URL: srv.URL, Token: "tok"}
}

const usageJSON = `{"turn_id":"turn-1","calls":2,"by_model":[{"provider":"scaleway","model":"glm-5.2","input_tokens":120,"cached_tokens":30,"output_tokens":15}]}`

func TestATurnCarriesTheUsageLaltersDoorReports(t *testing.T) {
	a := door(t, `{"jsonrpc":"2.0","id":1,"result":{"kind":"task","status":{"state":"completed"},`+
		`"artifacts":[{"name":"answer","parts":[{"kind":"text","text":"hi"}]}],"metadata":{"usage":`+usageJSON+`}}}`)
	text, turn, err := Say(context.Background(), a, Request{Text: "hello"})
	if err != nil || text != "hi" {
		t.Fatalf("text %q err %v", text, err)
	}
	u := turn.Usage
	if u == nil || u.TurnID != "turn-1" || u.Calls != 2 || len(u.ByModel) != 1 {
		t.Fatalf("usage = %+v", u)
	}
	if m := u.ByModel[0]; m.Provider != "scaleway" || m.Model != "glm-5.2" || m.InputTokens != 120 || m.CachedTokens != 30 || m.OutputTokens != 15 {
		t.Fatalf("model usage = %+v", m)
	}
}

func TestAFailedTurnStillReportsWhatItConsumed(t *testing.T) {
	a := door(t, `{"jsonrpc":"2.0","id":1,"result":{"kind":"task","status":{"state":"failed"},"metadata":{"usage":`+usageJSON+`}}}`)
	_, turn, err := Say(context.Background(), a, Request{Text: "hello"})
	if err == nil || turn.Usage == nil || turn.Usage.Calls != 2 {
		t.Fatalf("err %v usage %+v", err, turn.Usage)
	}
}

func TestAnErroredTurnStillReportsWhatItConsumed(t *testing.T) {
	a := door(t, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom","data":{"usage":`+usageJSON+`}}}`)
	_, turn, err := Say(context.Background(), a, Request{Text: "hello"})
	if err == nil || turn.Usage == nil || turn.Usage.TurnID != "turn-1" {
		t.Fatalf("err %v usage %+v", err, turn.Usage)
	}
}

func TestAnAgentReachedDirectlyReportsNoUsage(t *testing.T) {
	a := door(t, `{"jsonrpc":"2.0","id":1,"result":{"kind":"task","status":{"state":"completed"},"artifacts":[]}}`)
	_, turn, err := Say(context.Background(), a, Request{Text: "hello"})
	if err != nil || turn.Usage != nil {
		t.Fatalf("err %v usage %+v", err, turn.Usage)
	}
}
