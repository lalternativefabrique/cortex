package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestATurnsHeadersReachTheServerOnlyForThatTurn(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
	}))
	defer srv.Close()
	tr := newHTTPTransport(ServerConfig{URL: srv.URL})

	if _, err := tr.call(WithHeaders(context.Background(), map[string]string{"Authorization": "Bearer grant-marie"}), "tools/call", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.call(context.Background(), "tools/call", nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "Bearer grant-marie" || got[1] != "" {
		t.Fatalf("Authorization per call = %q", got)
	}
}

func TestANotificationAnsweredAcceptedIsDelivered(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		if err := newHTTPTransport(ServerConfig{URL: srv.URL}).notify(context.Background(), "notifications/initialized", nil); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
		srv.Close()
	}
}
