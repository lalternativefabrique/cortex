package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestReadReturnsNumberedLines(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "a.txt", "first\nsecond\n")
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "a.txt"})
	if !strings.Contains(out, "1\tfirst") || !strings.Contains(out, "2\tsecond") {
		t.Fatalf("lines are not numbered: %q", out)
	}
}

func TestReadPagesWithOffsetAndLimit(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeTemp(t, root, "long.txt", b.String())

	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{
		"path": "long.txt", "offset": 10, "limit": 3,
	})
	if !strings.Contains(out, "10\tline 10") {
		t.Fatalf("offset ignored: %q", out)
	}
	if strings.Contains(out, "13\tline 13") {
		t.Fatalf("limit ignored: %q", out)
	}
}

func TestReadTellsWhenMoreRemains(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	writeTemp(t, root, "long.txt", b.String())

	out := run(t, NewRead(ReadConfig{Root: root, MaxLines: 10}), map[string]any{"path": "long.txt"})
	if !strings.Contains(out, "offset=11") {
		t.Fatalf("truncated read does not say how to continue: %q", out)
	}
}

func TestReadRefusesBinaryFile(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "blob.bin", "text\x00more\n")
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "blob.bin"})
	if !strings.Contains(out, "binary") {
		t.Fatalf("binary file was not refused: %q", out)
	}
}

func TestReadRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "../../etc/passwd"})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("path escape was not refused: %q", out)
	}
}

func TestReadReportsMissingFile(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "nope.txt"})
	if !strings.Contains(out, "does not exist") {
		t.Fatalf("missing file was not reported clearly: %q", out)
	}
}

func TestReadRefusesDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(root+"/sub", 0o755); err != nil {
		t.Fatal(err)
	}
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "sub"})
	if !strings.Contains(out, "is a directory") {
		t.Fatalf("directory read was not refused: %q", out)
	}
}

func TestReadCutsOverlongLine(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "min.js", strings.Repeat("a", 5000)+"\n")
	out := run(t, NewRead(ReadConfig{Root: root, MaxLineBytes: 100}), map[string]any{"path": "min.js"})
	if !strings.Contains(out, "line cut") {
		t.Fatalf("long line was not cut: %q", out[:200])
	}
}

func TestReadMarksFileAsSeen(t *testing.T) {
	root := t.TempDir()
	path := writeTemp(t, root, "a.txt", "content\n")
	tracker := NewReadTracker()
	run(t, NewRead(ReadConfig{Root: root, Tracker: tracker}), map[string]any{"path": "a.txt"})
	if _, seen := tracker.ReadAt(path); !seen {
		t.Fatal("read did not record the file as seen, so edit would refuse it")
	}
}

func TestReadHandlesEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "empty.txt", "")
	out := run(t, NewRead(ReadConfig{Root: root}), map[string]any{"path": "empty.txt"})
	if !strings.Contains(out, "is empty") {
		t.Fatalf("empty file was not reported as such: %q", out)
	}
}
