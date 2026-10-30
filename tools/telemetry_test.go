package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func telemetryServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestNoGrantMeansNoTools(t *testing.T) {
	// A task that was lent no telemetry still repairs code; it must not be
	// offered tools that would fail on every call.
	if got := NewTelemetry(TelemetryConfig{}); got != nil {
		t.Fatalf("got %d tools with no grant, want none", len(got))
	}
	if got := NewTelemetry(TelemetryConfig{BaseURL: "https://api"}); got != nil {
		t.Fatal("a grant with no project produced tools")
	}
}

func TestTheThreeToolsReadTheirOwnEndpoint(t *testing.T) {
	var paths []string
	srv := telemetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"items":[]}`))
	})

	for _, tool := range NewTelemetry(TelemetryConfig{BaseURL: srv.URL, ProjectID: "p1", Token: "tok"}) {
		if _, err := tool.Execute(context.Background(), nil); err != nil {
			t.Fatalf("%s: %v", tool.Name(), err)
		}
	}

	want := []string{"/api/projects/p1/logs", "/api/projects/p1/traces", "/api/projects/p1/metrics"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i, w := range want {
		if paths[i] != w {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], w)
		}
	}
}

func TestTheGrantAuthenticatesTheRead(t *testing.T) {
	var auth string
	srv := telemetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	})

	tool := NewTelemetry(TelemetryConfig{BaseURL: srv.URL, ProjectID: "p1", Token: "granted"})[0]
	if _, err := tool.Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer granted" {
		t.Errorf("Authorization = %q, want the grant", auth)
	}
}

func TestArgumentsReachTheQueryTheAPIUnderstands(t *testing.T) {
	var q url.Values
	srv := telemetryServer(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = w.Write([]byte(`{}`))
	})

	tool := NewTelemetry(TelemetryConfig{BaseURL: srv.URL, ProjectID: "p1"})[0]
	args, _ := json.Marshal(telemetryArgs{
		Query: "deadline", Severity: "ERROR",
		From: "2026-09-05T01:00:00Z", To: "2026-09-05T02:00:00Z", Limit: 10,
	})
	if _, err := tool.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}

	// These names are the API's, not ours: a query sent under a name it does
	// not read is silently ignored and the agent sees the wrong window.
	for name, want := range map[string]string{
		"q": "deadline", "severity": "ERROR",
		"from": "2026-09-05T01:00:00Z", "to": "2026-09-05T02:00:00Z", "limit": "10",
	} {
		if got := q.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestARefusedReadIsAFindingNotACrash(t *testing.T) {
	srv := telemetryServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	// The agent must be able to carry on and say what it could not read;
	// an error return would end the run instead.
	tool := NewTelemetry(TelemetryConfig{BaseURL: srv.URL, ProjectID: "p1"})[0]
	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("a refusal ended the run: %v", err)
	}
	if !strings.Contains(res.Content, "not allowed") {
		t.Errorf("content = %q, want it to say the read was refused", res.Content)
	}
}
