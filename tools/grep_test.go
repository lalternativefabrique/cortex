package tools

import (
	"strings"
	"testing"
)

// withoutRipgrep forces the native backend by emptying PATH, so both search
// implementations are covered on any machine.
func withoutRipgrep(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "")
}

func TestGrepFindsMatchesWithLineNumbers(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "main.go", "package main\n\nfunc target() {}\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "func target"})
	if !strings.Contains(out, "main.go") {
		t.Fatalf("file path missing: %q", out)
	}
	if !strings.Contains(out, "3") {
		t.Fatalf("line number missing: %q", out)
	}
}

func TestGrepFindsMatchesWithoutRipgrep(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "main.go", "package main\n\nfunc target() {}\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "func target"})
	if strings.Contains(out, "not installed") {
		t.Fatal("the tool gave up instead of falling back to a native search")
	}
	if !strings.Contains(out, "main.go") {
		t.Fatalf("native search found nothing: %q", out)
	}
}

func TestGrepIsCaseInsensitiveByDefault(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "a.go", "TargetSymbol\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "targetsymbol"})
	if !strings.Contains(out, "a.go") {
		t.Fatalf("default search was case-sensitive: %q", out)
	}
}

func TestGrepHonorsCaseSensitive(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "a.go", "TargetSymbol\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{
		"pattern": "targetsymbol", "case_sensitive": true,
	})
	if !strings.Contains(out, "No match") {
		t.Fatalf("case_sensitive was ignored: %q", out)
	}
}

func TestGrepReportsNoMatch(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "a.go", "nothing here\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "absent_symbol"})
	if !strings.Contains(out, "No match") {
		t.Fatalf("empty result was not stated plainly: %q", out)
	}
}

func TestGrepSkipsIgnoredDirectories(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "node_modules/pkg/a.js", "needle\n")
	writeTemp(t, root, "src/b.js", "needle\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "needle"})
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules was searched: %q", out)
	}
	if !strings.Contains(out, "src/b.js") {
		t.Fatalf("source file was missed: %q", out)
	}
}

func TestGrepFiltersByGlob(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "a.go", "needle\n")
	writeTemp(t, root, "b.ts", "needle\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{
		"pattern": "needle", "glob": "*.go",
	})
	if !strings.Contains(out, "a.go") || strings.Contains(out, "b.ts") {
		t.Fatalf("glob was not applied: %q", out)
	}
}

func TestGrepCapsMatches(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "many.txt", strings.Repeat("needle\n", 50))

	out := run(t, NewGrep(GrepConfig{Root: root, MaxMatches: 5}), map[string]any{"pattern": "needle"})
	if !strings.Contains(out, "stopped at 5 matches") {
		t.Fatalf("cap was not announced: %q", out)
	}
}

func TestGrepSkipsBinaryFiles(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "blob.bin", "needle\x00binary\n")
	writeTemp(t, root, "text.go", "needle\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "needle"})
	if strings.Contains(out, "blob.bin") {
		t.Fatalf("a binary file was reported: %q", out)
	}
	if !strings.Contains(out, "text.go") {
		t.Fatalf("the text file was missed: %q", out)
	}
}

func TestGrepReportsInvalidPattern(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "("})
	if !strings.Contains(out, "error:") {
		t.Fatalf("an invalid regular expression was not reported: %q", out)
	}
}

func TestGrepRefusesPathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{
		"pattern": "x", "path": "../..",
	})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("path escape was not refused: %q", out)
	}
}

func TestGrepFilesOnlyReturnsPaths(t *testing.T) {
	withoutRipgrep(t)
	root := t.TempDir()
	writeTemp(t, root, "a.go", "needle\nneedle\nneedle\n")

	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{
		"pattern": "needle", "files_only": true,
	})
	if strings.Count(out, "a.go") != 1 {
		t.Fatalf("files_only should list each file once: %q", out)
	}
}
