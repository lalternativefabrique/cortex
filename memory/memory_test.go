package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

func withXDGState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "commit", "--allow-empty", "-q", "-m", "init")
	return root
}

func TestProjectKeyUsesOriginRemoteWhenPresent(t *testing.T) {
	root := gitInit(t)
	gitRun(t, root, "remote", "add", "origin", "git@github.com:example/repo.git")

	got := ProjectKey(root)
	if got != "git@github.com:example/repo.git" {
		t.Fatalf("ProjectKey() = %q, want the origin URL", got)
	}
}

func TestProjectKeyFallsBackToFirstCommitWithoutRemote(t *testing.T) {
	root := gitInit(t)

	got := ProjectKey(root)
	if got == root || got == "" {
		t.Fatalf("ProjectKey() = %q, want the first commit hash", got)
	}

	// Stable across calls, and across a worktree-like second checkout
	// sharing the same history: same repo, same key.
	if got2 := ProjectKey(root); got2 != got {
		t.Fatalf("ProjectKey() is not stable: %q vs %q", got, got2)
	}
}

func TestProjectKeyFallsBackToRootWithoutGit(t *testing.T) {
	root := t.TempDir()
	if got := ProjectKey(root); got != root {
		t.Fatalf("ProjectKey() = %q, want root %q for a non-git directory", got, root)
	}
}

func TestStoreAppendThenLoadRoundTrips(t *testing.T) {
	withXDGState(t)
	root := t.TempDir()

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.MemoryExtraction{Decisions: []string{"picked PostgreSQL"}}); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != 1 || got.Decisions[0] != "picked PostgreSQL" {
		t.Fatalf("Load() = %+v, want the appended decision", got)
	}
}

func TestLoadOnEmptyProjectReturnsZeroValue(t *testing.T) {
	withXDGState(t)
	root := t.TempDir()

	got, err := Load(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsEmpty() {
		t.Fatalf("Load() on a project with no memory = %+v, want empty", got)
	}
}

func TestLoadDedupesAndKeepsMostRecent(t *testing.T) {
	withXDGState(t)
	root := t.TempDir()

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.MemoryExtraction{Constraints: []string{"no EU hosting"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(agent.MemoryExtraction{Constraints: []string{"no EU hosting", "no external DB"}}); err != nil {
		t.Fatal(err)
	}

	got, err := Load(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Constraints) != 2 {
		t.Fatalf("Load() constraints = %v, want 2 unique entries", got.Constraints)
	}
}

func TestLoadCapsAtMaxItems(t *testing.T) {
	withXDGState(t)
	root := t.TempDir()

	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		d := []string{"decision " + string(rune('a'+i))}
		if err := store.Append(agent.MemoryExtraction{Decisions: d}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Load(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != 2 {
		t.Fatalf("Load(maxItems=2) = %d decisions, want 2", len(got.Decisions))
	}
	if got.Decisions[0] != "decision e" {
		t.Fatalf("Load(maxItems=2)[0] = %q, want the most recent decision first", got.Decisions[0])
	}
}

func TestStoreScopesByProjectKey(t *testing.T) {
	withXDGState(t)
	rootA := t.TempDir()
	rootB := t.TempDir()

	storeA, err := NewStore(rootA)
	if err != nil {
		t.Fatal(err)
	}
	if err := storeA.Append(agent.MemoryExtraction{Decisions: []string{"A only"}}); err != nil {
		t.Fatal(err)
	}

	got, err := Load(rootB, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsEmpty() {
		t.Fatalf("Load(rootB) = %+v, want empty — memory must not leak across projects", got)
	}
}

func TestDirIsUnderXDGStateHome(t *testing.T) {
	base := withXDGState(t)

	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(base, "skode", "memory")
	if dir != want {
		t.Fatalf("Dir() = %q, want %q", dir, want)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Dir() did not create %q", dir)
	}
}
