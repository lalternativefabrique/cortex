package tools

import (
	"os"
	"strings"
	"testing"
)

func TestGlobMatchesAcrossDirectories(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "a/b/deep.go", "package b\n")
	writeTemp(t, root, "top.go", "package main\n")

	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(out, "a/b/deep.go") {
		t.Fatalf("** did not match across directories: %q", out)
	}
}

func TestGlobMatchesSingleSegment(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "top.go", "package main\n")
	writeTemp(t, root, "nested/inner.go", "package nested\n")

	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{"pattern": "*.go"})
	if !strings.Contains(out, "top.go") {
		t.Fatalf("top-level file not matched: %q", out)
	}
	if strings.Contains(out, "nested/inner.go") {
		t.Fatalf("* leaked across a path separator: %q", out)
	}
}

func TestGlobSkipsIgnoredDirectories(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "node_modules/pkg/index.js", "module.exports = {}\n")
	writeTemp(t, root, "src/index.js", "export default 1\n")

	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{"pattern": "**/*.js"})
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules was walked: %q", out)
	}
	if !strings.Contains(out, "src/index.js") {
		t.Fatalf("source file was missed: %q", out)
	}
}

func TestGlobReportsNoMatch(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{"pattern": "**/*.rs"})
	if !strings.Contains(out, "No file matches") {
		t.Fatalf("empty result was not stated plainly: %q", out)
	}
}

func TestGlobCapsResults(t *testing.T) {
	root := t.TempDir()
	for i := range 20 {
		writeTemp(t, root, string(rune('a'+i))+".go", "package x\n")
	}
	out := run(t, NewGlob(GlobConfig{Root: root, MaxResults: 5}), map[string]any{"pattern": "*.go"})
	// The true total matters more than the cap: it tells the caller whether
	// the pattern was slightly too broad or pointed at the wrong tree.
	if !strings.Contains(out, "20 paths matched") {
		t.Fatalf("the real total was not reported: %q", out)
	}
	if !strings.Contains(out, "only the 5") {
		t.Fatalf("the cap was not announced: %q", out)
	}
}

func TestGlobRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{
		"pattern": "*.go", "path": "../..",
	})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("path escape was not refused: %q", out)
	}
}

func TestGlobSurvivesUnreadableSubtree(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can read any directory")
	}
	root := t.TempDir()
	writeTemp(t, root, "visible.go", "package x\n")
	writeTemp(t, root, "locked/hidden.go", "package y\n")
	if err := os.Chmod(root+"/locked", 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root+"/locked", 0o755) })

	out := run(t, NewGlob(GlobConfig{Root: root}), map[string]any{"pattern": "**/*.go"})
	if !strings.Contains(out, "visible.go") {
		t.Fatalf("an unreadable subtree aborted the whole search: %q", out)
	}
}
