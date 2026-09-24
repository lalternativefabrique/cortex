package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConnect_HTTPServer_CompletesHandshake(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		methods = append(methods, req.Method)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`))
		default:
			// notifications/initialized carries no id, but the server still
			// answers 200 with no body per synthiz's own behaviour.
		}
	}))
	defer srv.Close()

	c, err := Connect(context.Background(), ServerConfig{Name: "synthiz", URL: srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer c.Close()

	if len(methods) != 2 || methods[0] != "initialize" || methods[1] != "notifications/initialized" {
		t.Fatalf("methods = %v, want [initialize notifications/initialized]", methods)
	}
}

// A notification has no id at all — not id:0 — because synthiz's server (and
// the JSON-RPC spec) reads id:0 as a real request awaiting a reply.
func TestHTTPTransport_NotificationOmitsID(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := newHTTPTransport(ServerConfig{Name: "s", URL: srv.URL, Timeout: time.Second})
	if err := tr.notify(context.Background(), "notifications/initialized", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, hasID := raw["id"]; hasID {
		t.Fatal("a notification must omit id entirely, not send id:0")
	}
}

// synthiz's server always answers 200, even for a failed RPC — the failure
// lives in the "error" field of an otherwise-normal response body.
func TestHTTPTransport_RPCErrorInA200IsReportedAsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer srv.Close()

	tr := newHTTPTransport(ServerConfig{Name: "s", URL: srv.URL, Timeout: time.Second})
	_, err := tr.call(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("want an error for a JSON-RPC error response")
	}
	var rpcErr *rpcError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v, want *rpcError", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("code = %d, want -32601", rpcErr.Code)
	}
}

// A non-200 means the transport itself broke (wrong URL, server down,
// proxy error) — distinct from an RPC that reached the server and failed.
func TestHTTPTransport_NonOKStatusIsATransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	tr := newHTTPTransport(ServerConfig{Name: "s", URL: srv.URL, Timeout: time.Second})
	if _, err := tr.call(context.Background(), "tools/list", nil); err == nil {
		t.Fatal("want an error for a non-200 status")
	}
}

func TestHTTPTransport_SendsContentTypeJSON(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	tr := newHTTPTransport(ServerConfig{Name: "s", URL: srv.URL, Timeout: time.Second})
	if _, err := tr.call(context.Background(), "tools/list", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
}

// Statelessness: nothing about one call should require a prior call on the
// same *http.Client to have happened first, or a session to be carried.
func TestHTTPTransport_EachCallIsIndependent(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	}))
	defer srv.Close()

	tr := newHTTPTransport(ServerConfig{Name: "s", URL: srv.URL, Timeout: time.Second})
	for i := 0; i < 3; i++ {
		if _, err := tr.call(context.Background(), "tools/list", nil); err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 independent POSTs", calls)
	}
}

func TestConnect_RejectsBothCommandAndURL(t *testing.T) {
	_, err := Connect(context.Background(), ServerConfig{Name: "s", Command: "x", URL: "http://example.test"})
	if err == nil {
		t.Fatal("want an error when both Command and URL are set")
	}
}

func TestConnect_RejectsNeitherCommandNorURL(t *testing.T) {
	_, err := Connect(context.Background(), ServerConfig{Name: "s"})
	if err == nil {
		t.Fatal("want an error when neither Command nor URL is set")
	}
}
