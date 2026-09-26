package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func messagesOf(records []map[string]any) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r["msg"].(string)
	}
	return out
}

func TestRunLogsWhatTheAgentDoesWithoutContent(t *testing.T) {
	var buf bytes.Buffer
	tool := &fakeTool{name: "probe", result: "secret tool output"}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{"q":"secret argument"}`)}}, Usage: Usage{Input: 10, Output: 2}},
		{Text: "secret answer", Usage: Usage{Input: 20, Output: 5}},
	}}
	r, err := NewRunner(Config{
		Client: client,
		Tools:  []Tool{tool},
		Logger: slog.New(slog.NewJSONHandler(&buf, nil)).With("task_id", "t-1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "secret question"}}); err != nil {
		t.Fatal(err)
	}

	records := logRecords(t, &buf)
	got := strings.Join(messagesOf(records), " | ")
	want := "agent: turn started | agent: model answered | agent: tool called | agent: model answered | agent: turn ended"
	if got != want {
		t.Fatalf("log sequence:\n got %s\nwant %s", got, want)
	}
	for _, rec := range records {
		if rec["task_id"] != "t-1" {
			t.Fatalf("%q lost the host scope: %v", rec["msg"], rec)
		}
	}
	if records[2]["tool"] != "probe" || records[2]["result_bytes"] != float64(len("secret tool output")) {
		t.Fatalf("tool record = %v", records[2])
	}
	end := records[4]
	if end["steps"] != float64(2) || end["tool_calls"] != float64(1) || end["tokens_in"] != float64(30) || end["tokens_out"] != float64(7) {
		t.Fatalf("turn ended record = %v", end)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("content leaked into the logs:\n%s", buf.String())
	}
}

func TestRunLogsToolFailureAndFailedTurn(t *testing.T) {
	var buf bytes.Buffer
	tool := &fakeTool{name: "probe", err: errors.New("boom")}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "probe", Arguments: json.RawMessage(`{}`)}}},
		{Text: "ok"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	records := logRecords(t, &buf)
	if records[2]["msg"] != "agent: tool failed" || records[2]["level"] != "WARN" || records[2]["error"] != "boom" {
		t.Fatalf("tool failure record = %v", records[2])
	}

	buf.Reset()
	failing := &scriptedClient{err: errors.New("model down")}
	r, err = NewRunner(Config{Client: failing, Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}); err == nil {
		t.Fatal("Run succeeded against a failing model")
	}
	records = logRecords(t, &buf)
	last := records[len(records)-1]
	if last["msg"] != "agent: turn failed" || last["level"] != "ERROR" {
		t.Fatalf("last record = %v", last)
	}
}

func TestRunLogsTruncatedTurn(t *testing.T) {
	var buf bytes.Buffer
	tool := &fakeTool{name: "probe", result: "x"}
	call := CompletionResponse{ToolCalls: []ToolCall{{ID: "c", Name: "probe", Arguments: json.RawMessage(`{}`)}}}
	client := &scriptedClient{responses: []CompletionResponse{call, call}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{tool}, MaxSteps: 2, Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	records := logRecords(t, &buf)
	last := records[len(records)-1]
	if last["msg"] != "agent: turn truncated" || last["max_steps"] != float64(2) {
		t.Fatalf("last record = %v", last)
	}
}
