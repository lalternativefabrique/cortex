package promptctx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/memory"
	"github.com/lalternative/packages/go/cortex/session"
)

func fixedTime(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
}

func TestSystemIncludesBaseInstructions(t *testing.T) {
	got, err := System(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "coding agent") {
		t.Fatalf("base instructions are missing: %q", got)
	}
}

func TestSystemAppendsProjectConventions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Always run gofmt."), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Always run gofmt.") {
		t.Fatal("project conventions were not injected")
	}
	if !strings.Contains(got, "AGENTS.md") {
		t.Fatal("the conventions are not attributed to their file")
	}
}

func TestSystemMergesEveryConventionFile(t *testing.T) {
	// A repository often carries several, and they drift. Taking only the
	// first hides the others — including the one that is current.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("from agents"), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("from claude"), 0o644)

	got, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "from agents") || !strings.Contains(got, "from claude") {
		t.Fatalf("both files should be present: %q", got)
	}
	if !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, "CLAUDE.md") {
		t.Fatal("each block should say which file it came from, so a contradiction is attributable")
	}
}

func TestSystemWarnsAboutContradictions(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("current"), 0o644)

	got, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "disagree") {
		t.Fatal("nothing tells the model what to do when two convention files contradict each other")
	}
}

func TestSystemIsStableAcrossCalls(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("conventions"), 0o644)

	first, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the system prefix varies between calls, which defeats the server's prefix cache for every run")
	}
}

func TestSystemCarriesNothingVolatile(t *testing.T) {
	got, err := System(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// A clock, a hostname or a commit hash in the stable prefix invalidates
	// the cache on every single call.
	for _, marker := range []string{"2026", "20:", "Branch: "} {
		if strings.Contains(got, marker) {
			t.Fatalf("the stable prefix appears to contain volatile data (%q)", marker)
		}
	}
}

func TestSystemHonorsOverride(t *testing.T) {
	got, err := System(Options{Root: t.TempDir(), Override: "custom instructions"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "custom instructions") {
		t.Fatalf("override was ignored: %q", got)
	}
}

func TestSystemInjectsProjectMemory(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()

	store, err := memory.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.MemoryExtraction{
		Decisions:   []string{"picked PostgreSQL"},
		Constraints: []string{"no EU hosting"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "picked PostgreSQL") || !strings.Contains(got, "no EU hosting") {
		t.Fatalf("project memory was not injected: %q", got)
	}
	if !strings.Contains(got, "Project memory") {
		t.Fatal("the section is not labelled, so the model cannot tell it apart from the current conversation")
	}
}

func TestSystemOmitsProjectMemorySectionWhenNoneStored(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	got, err := System(Options{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Project memory") {
		t.Fatalf("a memory section appeared for a project with none: %q", got)
	}
}

func TestSystemInjectsRecentSessionsWhenRequested(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()

	store, _, err := session.Create(fixedTime(t), root, "test-model", "fix the flaky test in pkg/foo")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	got, err := System(Options{Root: root, RecentSessions: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "fix the flaky test in pkg/foo") {
		t.Fatalf("recent session summary was not injected: %q", got)
	}
}

func TestSystemOmitsRecentSessionsWhenNotRequested(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()

	store, _, err := session.Create(fixedTime(t), root, "test-model", "some earlier task")
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	got, err := System(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "some earlier task") {
		t.Fatal("recent sessions were injected despite RecentSessions being zero")
	}
}

func TestWorkspaceReportsRootAndTree(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main"), 0o644)
	os.Mkdir(filepath.Join(root, "pkg"), 0o755)

	got := Workspace(root)
	if !strings.Contains(got, root) {
		t.Fatal("the workspace path is missing")
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "pkg/") {
		t.Fatalf("the top level listing is incomplete: %q", got)
	}
}

func TestWorkspaceReportsGitState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(root, "dirty.go"), []byte("package main"), 0o644)

	got := Workspace(root)
	if !strings.Contains(got, "Branch:") {
		t.Fatalf("branch is missing: %q", got)
	}
	if !strings.Contains(got, "dirty.go") {
		t.Fatalf("modified files are not reported: %q", got)
	}
}

func TestWorkspaceSurvivesOutsideGit(t *testing.T) {
	got := Workspace(t.TempDir())
	if got == "" {
		t.Fatal("Workspace returned nothing for a non-git directory")
	}
}
