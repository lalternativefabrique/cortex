package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func TestCreateAndLoadRoundTrip(t *testing.T) {
	isolate(t)
	now := time.Date(2026, 8, 21, 14, 30, 0, 0, time.UTC)

	store, id, err := Create(now, "/work/repo", "devstral", "fix the parser")
	if err != nil {
		t.Fatal(err)
	}
	messages := []agent.Message{
		{Role: agent.RoleUser, Content: "fix the parser"},
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "c1", Name: "read", Arguments: []byte(`{"path":"p.go"}`)}}},
		{Role: agent.RoleTool, ToolCallID: "c1", Content: "file contents"},
	}
	for _, m := range messages {
		if err := store.Append(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != "/work/repo" || got.Model != "devstral" || got.Prompt != "fix the parser" {
		t.Fatalf("header lost: %+v", got)
	}
	if len(got.Messages) != len(messages) {
		t.Fatalf("got %d messages, want %d", len(got.Messages), len(messages))
	}
	if got.Messages[1].ToolCalls[0].Name != "read" {
		t.Fatal("tool calls did not survive the round trip")
	}
	if got.Messages[2].ToolCallID != "c1" {
		t.Fatal("tool_call_id did not survive, so the resumed history would be rejected")
	}
}

func TestLoadToleratesTruncatedFinalLine(t *testing.T) {
	dir := isolate(t)
	store, id, err := Create(time.Now(), "/work", "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.Message{Role: agent.RoleUser, Content: "complete"}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Simulate a run killed mid-write.
	path := filepath.Join(dir, "skode", "sessions", id+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"type":"message","message":{"role":"assi`)
	f.Close()

	got, err := Load(id)
	if err != nil {
		t.Fatalf("a partial final line made the whole session unreadable: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("got %d messages, want the 1 complete one", len(got.Messages))
	}
}

func TestAppendSurvivesWithoutClose(t *testing.T) {
	isolate(t)
	store, id, err := Create(time.Now(), "/work", "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.Message{Role: agent.RoleUser, Content: "written"}); err != nil {
		t.Fatal(err)
	}
	// Deliberately no Close: an interrupted run must still leave a resumable
	// file, which buffering without a per-record flush would not.
	got, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatal("the message was buffered away and lost on interrupt")
	}
}

func TestReopenAppendsToExistingSession(t *testing.T) {
	dir := isolate(t)
	store, id, err := Create(time.Now(), "/work", "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	store.Append(agent.Message{Role: agent.RoleUser, Content: "first"})
	store.Close()

	path := filepath.Join(dir, "skode", "sessions", id+".jsonl")
	again, err := Reopen(path)
	if err != nil {
		t.Fatal(err)
	}
	again.Append(agent.Message{Role: agent.RoleAssistant, Content: "second"})
	again.Close()

	got, err := Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("got %d messages, want 2", len(got.Messages))
	}
	if got.Root != "/work" {
		t.Fatal("reopening lost the header")
	}
}

func TestListReturnsMostRecentFirst(t *testing.T) {
	isolate(t)
	for i, ts := range []time.Time{
		time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC),
	} {
		store, _, err := Create(ts, "/work", "m", strings.Repeat("t", i+1))
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}

	got, err := List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d sessions, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Started.After(got[i-1].Started) {
			t.Fatal("sessions are not ordered most recent first")
		}
	}
}

func TestListHonorsLimit(t *testing.T) {
	isolate(t)
	for i := range 5 {
		store, _, err := Create(time.Date(2026, 8, 21, 10, i, 0, 0, time.UTC), "/w", "m", "t")
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
	got, err := List(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2", len(got))
	}
}

func TestLoadReportsMissingSession(t *testing.T) {
	isolate(t)
	_, err := Load("20260101-000000")
	if err == nil {
		t.Fatal("loading a missing session succeeded")
	}
	if !strings.Contains(err.Error(), "-sessions") {
		t.Fatalf("error does not point at how to list sessions: %v", err)
	}
}

func TestSessionFileIsPrivate(t *testing.T) {
	dir := isolate(t)
	store, id, err := Create(time.Now(), "/work", "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	info, err := os.Stat(filepath.Join(dir, "skode", "sessions", id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 — transcripts carry repository contents", info.Mode().Perm())
	}
}

func TestASessionRemembersTheBranchesItPassedThrough(t *testing.T) {
	// Work begun on main and moved onto a feature branch is one session, and
	// where it left off is the last branch it saw — not the one it opened on.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	store, id, err := Create(time.Now(), t.TempDir(), "m", "fix the thing")
	if err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"main", "main", "feat/thing"} {
		if err := store.AppendBranch(branch); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Append(agent.Message{Role: agent.RoleUser, Content: "fix the thing"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	listed, err := List(10)
	if err != nil {
		t.Fatal(err)
	}
	var found *Summary
	for i := range listed {
		if listed[i].ID == id {
			found = &listed[i]
		}
	}
	if found == nil {
		t.Fatal("the session that was just written is not listed")
	}
	// Twice on main is one move, not two: the journal reads as what changed.
	if got := found.Branches; len(got) != 2 || got[0] != "main" || got[1] != "feat/thing" {
		t.Fatalf("branches = %v, want [main feat/thing]", got)
	}
}
