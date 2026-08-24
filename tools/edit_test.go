package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

func writeTemp(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func markRead(t *testing.T, tracker *ReadTracker, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	tracker.MarkRead(path, info.ModTime())
}

func run(t *testing.T, tool agent.Tool, args any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned a hard error: %v", err)
	}
	return res.Content
}

func TestEditReplacesUniqueMatch(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "main.go", "package main\n\nfunc main() {}\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	tool := NewEdit(EditConfig{Root: root, Tracker: tracker})
	out := run(t, tool, map[string]any{
		"path":       "main.go",
		"old_string": "func main() {}",
		"new_string": "func main() { println(\"hi\") }",
	})
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("edit failed: %s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "println") {
		t.Fatalf("file was not changed: %q", got)
	}
}

func TestEditRefusesAmbiguousMatch(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "dup.go", "x := 1\nx := 1\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	out := run(t, NewEdit(EditConfig{Root: root, Tracker: tracker}), map[string]any{
		"path":       "dup.go",
		"old_string": "x := 1",
		"new_string": "x := 2",
	})
	if !strings.Contains(out, "matches 2 places") {
		t.Fatalf("ambiguous edit was not refused with a count: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "x := 1\nx := 1\n" {
		t.Fatalf("file was modified despite the refusal: %q", got)
	}
}

func TestEditReplaceAllAcceptsAmbiguity(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "dup.go", "x := 1\nx := 1\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	out := run(t, NewEdit(EditConfig{Root: root, Tracker: tracker}), map[string]any{
		"path":        "dup.go",
		"old_string":  "x := 1",
		"new_string":  "x := 2",
		"replace_all": true,
	})
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("replace_all failed: %s", out)
	}
	got, _ := os.ReadFile(path)
	if strings.Count(string(got), "x := 2") != 2 {
		t.Fatalf("replace_all did not replace both: %q", got)
	}
}

func TestEditRefusesUnreadFile(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "unread.go", "content\n")
	out := run(t, NewEdit(EditConfig{Root: root, Tracker: NewReadTracker()}), map[string]any{
		"path":       "unread.go",
		"old_string": "content",
		"new_string": "other",
	})
	if !strings.Contains(out, "has not been read") {
		t.Fatalf("edit of an unread file was allowed: %s", out)
	}
}

func TestEditRefusesFileChangedSinceRead(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "race.go", "original\n")
	tracker := NewReadTracker()
	tracker.MarkRead(path, time.Now().Add(-time.Hour))

	out := run(t, NewEdit(EditConfig{Root: root, Tracker: tracker}), map[string]any{
		"path":       "race.go",
		"old_string": "original",
		"new_string": "changed",
	})
	if !strings.Contains(out, "changed on disk") {
		t.Fatalf("stale edit was allowed: %s", out)
	}
}

func TestEditRefusesMissingMatch(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "a.go", "hello\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	out := run(t, NewEdit(EditConfig{Root: root, Tracker: tracker}), map[string]any{
		"path":       "a.go",
		"old_string": "not present",
		"new_string": "x",
	})
	if !strings.Contains(out, "was not found") {
		t.Fatalf("missing match was not reported: %s", out)
	}
}

func TestEditRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewEdit(EditConfig{Root: root, Tracker: NewReadTracker()}), map[string]any{
		"path":       "../escape.go",
		"old_string": "a",
		"new_string": "b",
	})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("path escape was not refused: %s", out)
	}
}

func TestEditHonorsDenial(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "guarded.go", "before\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	out := run(t, NewEdit(EditConfig{Root: root, Tracker: tracker, Approver: DenyAll{}}), map[string]any{
		"path":       "guarded.go",
		"old_string": "before",
		"new_string": "after",
	})
	if !strings.Contains(out, "read-only") {
		t.Fatalf("denial was not reported: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "before\n" {
		t.Fatalf("file changed despite denial: %q", got)
	}
}

func TestEditPreservesFileMode(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "script.sh", "echo before\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	run(t, NewEdit(EditConfig{Root: root, Tracker: tracker}), map[string]any{
		"path":       "script.sh",
		"old_string": "before",
		"new_string": "after",
	})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}
