package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpaceMapsWorkspaceDirectoriesAndRefusesOthers(t *testing.T) {
	s := &Space{Root: "/home/me/project"}
	for dir, want := range map[string]string{
		"":                         SpaceWorkdir,
		"/home/me/project":         SpaceWorkdir,
		"/home/me/project/apps/go": SpaceWorkdir + "/apps/go",
	} {
		if got, err := s.inside(dir); err != nil || got != want {
			t.Errorf("inside(%q) = %q, %v; want %q", dir, got, err, want)
		}
	}
	for _, dir := range []string{"/home/me", "/home/me/project-other", "/etc"} {
		if _, err := s.inside(dir); err == nil {
			t.Errorf("inside(%q) was accepted", dir)
		}
	}
}

// TestSpaceRunsCommandsWhereTheMachineIsOutOfReach opens a real agent space,
// so it runs only with SKLP_SPACE_TEST=1 and a sklp that knows "agent up".
func TestSpaceRunsCommandsWhereTheMachineIsOutOfReach(t *testing.T) {
	if os.Getenv("SKLP_SPACE_TEST") != "1" {
		t.Skip("set SKLP_SPACE_TEST=1 to open a real sklp agent space")
	}
	sklp, err := exec.LookPath("sklp")
	if err != nil {
		t.Skip("no sklp on PATH")
	}
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".sklp"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, ".sklp", "space.yaml"), []byte("runner:\n  base_image: docker.io/library/debian:trixie-slim\n  toolchain:\n    go: \"1.26.10\"\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=s3cr3t-value-123\n"), 0o600))
	must(t, os.MkdirAll(filepath.Join(root, "apps"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "probe.txt"), []byte("from the host\n"), 0o644))
	t.Setenv("LALTER_TOKEN", "host-token-value")

	s := &Space{Sklp: sklp, Root: root, StartTimeout: 10 * time.Minute}
	t.Cleanup(func() { _ = s.Close() })
	run := func(line, dir string) Output {
		t.Helper()
		out, err := s.Run(context.Background(), Command{Line: line, Dir: dir})
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		return out
	}

	if out := run("cat probe.txt", filepath.Join(root, "apps")); strings.TrimSpace(out.Stdout) != "from the host" {
		t.Errorf("the workspace is not visible from its directory: %+v", out)
	}
	if out := run("go version && sklp version", ""); !strings.Contains(out.Stdout, "go1.26") || !strings.Contains(out.Stdout, "sklp") {
		t.Errorf("the recipe's tools are missing: %+v", out)
	}
	if out := run("cat .env; env", ""); strings.Contains(out.Stdout, "s3cr3t-value-123") || strings.Contains(out.Stdout, "host-token-value") {
		t.Errorf("a secret reached the space: %q", out.Stdout)
	}
	if out := run("echo mode: yes > .sklp/space.yaml 2>&1; ls ~/.ssh 2>&1", ""); !strings.Contains(out.Stdout+out.Stderr, "Read-only") {
		t.Errorf("the agent could rewrite its workspace's .sklp: %+v", out)
	}
	run("echo made > apps/made.txt", "")
	if b, err := os.ReadFile(filepath.Join(root, "apps", "made.txt")); err != nil || strings.TrimSpace(string(b)) != "made" {
		t.Errorf("a file written in the space did not reach the workspace: %q %v", b, err)
	}
	if out := run("timeout 4 bash -c '</dev/tcp/10.0.2.2/22' 2>/dev/null && echo HOST-REACHED || true", ""); strings.Contains(out.Stdout, "HOST-REACHED") {
		t.Errorf("the host was reachable from the space: %q", out.Stdout)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
