package host

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	logs := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

func TestATaskLogsItsLifeFromStartToCompletion(t *testing.T) {
	logs := captureLogs(t)
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "scribe"}, Provider: provider, Token: "t"})
	res := rpc(t, srv.URL, "t", "message/send", message("bonjour", "ctx-log", map[string]any{SubjectKey: "person-1"}))
	res.Body.Close()

	var task []map[string]any
	for _, rec := range logs.records(t) {
		if rec["context_id"] == "ctx-log" {
			task = append(task, rec)
		}
	}
	var got []string
	for _, rec := range task {
		got = append(got, rec["msg"].(string))
		if rec["agent"] != "scribe" || rec["subject"] != "person-1" || rec["task_id"] == "" {
			t.Fatalf("%q lost the task scope: %v", rec["msg"], rec)
		}
	}
	want := "cortex: task started | cortex: task working | agent: turn started | agent: model answered | agent: turn ended | cortex: task completed"
	if strings.Join(got, " | ") != want {
		t.Fatalf("task log:\n got %s\nwant %s", strings.Join(got, " | "), want)
	}
	if strings.Contains(logsText(logs), "bonjour") {
		t.Fatal("the message reached the logs")
	}
}

func TestAFailedTaskLogsWhy(t *testing.T) {
	logs := captureLogs(t)
	provider, _ := fakeModel(t, "")
	srv := start(t, Config{Agent: Agent{Name: "scribe"}, Provider: provider, Token: "t"})
	res := rpc(t, srv.URL, "t", "message/send", message("", "ctx-empty", nil))
	res.Body.Close()

	for _, rec := range logs.records(t) {
		if rec["msg"] == "cortex: task failed" && rec["context_id"] == "ctx-empty" {
			if rec["level"] != "ERROR" || rec["error"] != "a text message is required" {
				t.Fatalf("failed record = %v", rec)
			}
			return
		}
	}
	t.Fatal("no cortex: task failed record")
}

func logsText(b *lockedBuffer) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
