package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are the bypasses the security audit found in the prefix matcher;
// each is pinned so the parser keeps closing it.

func TestPlanModeCannotWriteThroughAReadOnlyCommand(t *testing.T) {
	root := t.TempDir()
	g := &GatedApprover{Mode: ModePlan, Root: root}
	for line, want := range map[string]Refusal{
		"uniq rules.txt .ai/settings.local.yaml":      RefusedProtected,
		"sort \"-o\" .git/hooks/pre-commit payload":   RefusedProtected,
		"git config core.hooksPath /tmp/evil":         RefusedProtected,
		"git -c core.hooksPath=/tmp/evil commit -m x": RefusedProtected,
		"tree -o go.mod":                  RefusedByMode,
		"find . -name '*.go' \"-delete\"": RefusedByMode,
		`find . "-exec" rm x ;`:           RefusedByMode,
		"git branch \"-D\" main":          RefusedByMode,
		"rg \"--pre\" ./script x":         RefusedByMode,
		"sed -n 1p -e '1e id' file":       RefusedByMode,
		"sed -n 1p -ibak file":            RefusedByMode,
		"sed -n -e 'w out' file":          RefusedByMode,
		"go env -w GOFLAGS=-toolexec=./x": RefusedByMode,
		"go vet -vettool=./x ./...":       RefusedByMode,
		"ls > .ai/settings.local.yaml":    RefusedProtected,
	} {
		if got := refusalFor(t, g, bashReq(line)); got != want {
			t.Errorf("%q: got %v, want %v", line, got, want)
		}
	}
	for _, line := range []string{"git config --get user.name", "git config -l", "sed -n 1,20p file.go", "uniq -c counts", "grep -i todo ./...", "sort f | uniq", "ls 2>/dev/null"} {
		if d := decide(t, g, bashReq(line)); d != Allow {
			t.Errorf("%q was refused in plan mode", line)
		}
	}
}

func TestAlwaysAskHoldsAgainstChainingAndQuoting(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes}
	for _, line := range []string{
		"echo ok && git push origin main",
		"git \"push\" origin main",
		"git status; git push",
		"ls | git push",
	} {
		if got := refusalFor(t, g, bashReq(line)); got != RefusedUnattended {
			t.Errorf("%q: got %v, want the push put to a person", line, got)
		}
	}
}

func TestTheCommandListIsNotDodgedByExecFlags(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Commands: []string{"find", "go test"}}
	for _, line := range []string{
		`find . -exec sh -c 'id' ;`,
		"find . -execdir ./x {} +",
		"go test -exec ./evil ./...",
		"go test -toolexec ./evil ./...",
		"go test -toolexec=./evil ./...",
		"./evil.sh",
		"find . > .ai/settings.local.yaml",
	} {
		if d := decide(t, g, bashReq(line)); d != Deny {
			t.Errorf("%q ran under a command list of find and go test", line)
		}
	}
	if d := decide(t, g, bashReq("find . -name '*.go' | go test ./...")); d != Allow {
		t.Error("listed commands chained together were refused")
	}
}

func TestSecretGuardSeesThroughGlobs(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.WriteFile(filepath.Join(root, ".env"), []byte("K=v"), 0o600)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), []byte("k"), 0o600)
	g := SecretGuard{Next: AllowAll{}, Root: root}
	for _, line := range []string{"cat .en[v]", "cat ~/.ssh/*", "head -c 200 ~/.ssh/id_*", "cat < .env"} {
		if d, err := g.Approve(context.Background(), bashReq(line)); d != Deny || err != RefusedSecret {
			t.Errorf("%q: got %v %v, want a secret refusal", line, d, err)
		}
	}
	for _, line := range []string{"cat *.go", "ls ~/.ssh", "cat .env.example"} {
		if d, _ := g.Approve(context.Background(), bashReq(line)); d != Allow {
			t.Errorf("%q was refused", line)
		}
	}
}

func TestGrepRefusesASecretFileNamedAsItsPath(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=hunter2"), 0o600)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main // TOKEN"), 0o644)
	tool := NewGrep(GrepConfig{Root: root, Approver: SecretGuard{Next: AllowAll{}, Root: root}})
	out := run(t, tool, map[string]any{"pattern": "TOKEN", "path": ".env"})
	if !strings.Contains(out, "SECRET") || strings.Contains(out, "hunter2") {
		t.Fatalf("grep on .env returned %q", out)
	}
	if out := run(t, tool, map[string]any{"pattern": "TOKEN"}); !strings.Contains(out, "main.go") || strings.Contains(out, "hunter2") {
		t.Fatalf("workspace grep returned %q", out)
	}
}
