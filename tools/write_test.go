package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCreatesFileAndParents(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewWrite(WriteConfig{Root: root, Tracker: NewReadTracker()}), map[string]any{
		"path": "a/b/new.go", "content": "package b\n",
	})
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("write failed: %s", out)
	}
	got, err := os.ReadFile(filepath.Join(root, "a/b/new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package b\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriteRefusesOverwritingUnreadFile(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "existing.go", "original\n")
	out := run(t, NewWrite(WriteConfig{Root: root, Tracker: NewReadTracker()}), map[string]any{
		"path": "existing.go", "content": "replacement\n",
	})
	if !strings.Contains(out, "has not been read") {
		t.Fatalf("blind overwrite was allowed: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "original\n" {
		t.Fatalf("file was overwritten: %q", got)
	}
}

func TestWriteOverwritesAfterRead(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "existing.go", "original\n")
	tracker := NewReadTracker()
	markRead(t, tracker, path)

	out := run(t, NewWrite(WriteConfig{Root: root, Tracker: tracker}), map[string]any{
		"path": "existing.go", "content": "replacement\n",
	})
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("write failed: %s", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "replacement\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestWriteRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewWrite(WriteConfig{Root: root, Tracker: NewReadTracker()}), map[string]any{
		"path": "../escape.go", "content": "x",
	})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("path escape was not refused: %s", out)
	}
}

func TestWriteHonorsDenial(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewWrite(WriteConfig{
		Root: root, Tracker: NewReadTracker(), Approver: DenyAll{},
	}), map[string]any{"path": "new.go", "content": "x"})
	if !strings.Contains(out, "read-only") {
		t.Fatalf("denial was not reported: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); err == nil {
		t.Fatal("file was created despite denial")
	}
}

func TestWriteMarksFileAsSeen(t *testing.T) {
	root := t.TempDir()
	tracker := NewReadTracker()
	run(t, NewWrite(WriteConfig{Root: root, Tracker: tracker}), map[string]any{
		"path": "new.go", "content": "x",
	})
	if _, seen := tracker.ReadAt(filepath.Join(root, "new.go")); !seen {
		t.Fatal("a freshly written file should be editable without re-reading it")
	}
}
