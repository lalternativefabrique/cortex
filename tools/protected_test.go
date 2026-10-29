package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGatedApproverRefusesWritesToItsOwnRulesWhateverTheMode(t *testing.T) {
	root := t.TempDir()
	allowAll, err := ParseRule("write")
	if err != nil {
		t.Fatal(err)
	}
	g := &GatedApprover{Mode: ModeYes, Root: root, Ask: AllowAll{}, Rules: Rules{Allow: []Rule{allowAll}}}
	for _, scope := range []string{
		"write:" + filepath.Join(root, ".ai/settings.yaml"),
		"edit:" + filepath.Join(root, ".ai/settings.local.yaml"),
		"write:" + filepath.Join(root, ".ai/mcp.yaml"),
		"write:" + filepath.Join(root, ".git/hooks/pre-commit"),
		"write:" + filepath.Join(root, ".claude/settings.json"),
	} {
		d, err := g.Approve(context.Background(), Request{Tool: "write", Scope: scope})
		if d != Deny || err != RefusedProtected {
			t.Errorf("%s: got %v %v, want a protected refusal", scope, d, err)
		}
	}
	d, _ := g.Approve(context.Background(), Request{Tool: "write", Scope: "write:" + filepath.Join(root, "main.go")})
	if d != Allow {
		t.Error("an ordinary file of the workspace was refused")
	}
}

func TestGatedApproverRefusesAWriteThroughALinkToAProtectedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ai/settings.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".ai/settings.yaml"), filepath.Join(root, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	g := &GatedApprover{Mode: ModeYes, Root: root, Ask: AllowAll{}}
	if d, _ := g.Approve(context.Background(), Request{Tool: "write", Scope: "write:" + filepath.Join(root, "notes.txt")}); d != Deny {
		t.Fatal("a link inside the workspace let a write reach the agent's settings")
	}
}

func TestGatedApproverRefusesShellWritesToProtectedPaths(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Root: t.TempDir(), Ask: AllowAll{}}
	for _, line := range []string{
		"echo 'mode: yes' > .ai/settings.yaml",
		"ls && sed -i s/ask/allow/ .ai/settings.local.yaml",
		"cp evil.yaml .ai/mcp.yaml",
		"echo key >> ~/.ssh/authorized_keys",
		"tee $HOME/.config/skode/settings.yaml < x",
	} {
		if d, err := g.Approve(context.Background(), Request{Tool: "bash", Action: line}); d != Deny || err != RefusedProtected {
			t.Errorf("%q: got %v %v, want a protected refusal", line, d, err)
		}
	}
	for _, line := range []string{"cat .ai/settings.yaml 2>&1", "git status", "grep -r cpu .ai/settings.yaml"} {
		if d, _ := g.Approve(context.Background(), Request{Tool: "bash", Action: line}); d != Allow {
			t.Errorf("%q: a read was refused", line)
		}
	}
}

func TestResolveWithinRootRefusesALinkLeadingOutside(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWithinRoot(root, "escape/authorized_keys"); err == nil {
		t.Fatal("a symbolic link let a path leave the workspace")
	}
	if _, err := resolveWithinRoot(root, "new/dir/file.go"); err != nil {
		t.Fatalf("a path not yet created was refused: %v", err)
	}
}
