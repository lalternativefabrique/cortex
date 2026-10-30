package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpillKeepsWhatTruncationDrops(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	body := strings.Repeat("a", 100) + "THE MIDDLE" + strings.Repeat("b", 100)
	path, err := spill(body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatal("the spilled file does not hold the whole output")
	}
}

func TestSameOutputSpillsOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	first, _ := spill("identical")
	second, _ := spill("identical")
	if first != second {
		t.Fatalf("%q and %q, want one file for one output", first, second)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "skode", "output"))
	if len(entries) != 1 {
		t.Fatalf("%d files, want 1", len(entries))
	}
}

func TestSpillStaysOutOfTheWorkspace(t *testing.T) {
	// A long test run turning up in git status would be a change the operator
	// did not make.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path, err := spill("anything")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, filepath.Join("skode", "output")) {
		t.Fatalf("path = %q, want it under the state directory", path)
	}
}

func TestShortOutputIsNotSpilled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	tool := &bashTool{cfg: BashConfig{MaxOutputBytes: 1000}}
	if got := tool.fit("short"); got != "short" {
		t.Fatalf("fit = %q, want it untouched", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "skode", "output")); !os.IsNotExist(err) {
		t.Fatal("output that fits was written to disk anyway")
	}
}

func TestTruncatedOutputNamesItsFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tool := &bashTool{cfg: BashConfig{MaxOutputBytes: 200}}
	body := strings.Repeat("x", 500) + "FAILED: the second one" + strings.Repeat("y", 500)
	got := tool.fit(body)
	if strings.Contains(got, "FAILED: the second one") {
		t.Fatal("the fixture is not long enough to lose its middle")
	}
	if !strings.Contains(got, "in full at ") {
		t.Fatalf("fit = %q, want the file named", got)
	}
	start := strings.Index(got, "in full at ") + len("in full at ")
	end := strings.Index(got[start:], " —")
	full, err := os.ReadFile(got[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "FAILED: the second one") {
		t.Fatal("what truncation dropped is not in the file it points at")
	}
}

func TestSpillDirectoryIsBounded(t *testing.T) {
	// Nothing else deletes from it, so a week of truncated test runs would
	// otherwise accumulate without limit.
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	for i := 0; i < maxSpillFiles+10; i++ {
		if _, err := spill(strings.Repeat("x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "skode", "output"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > maxSpillFiles {
		t.Fatalf("%d files, want at most %d", len(entries), maxSpillFiles)
	}
}
