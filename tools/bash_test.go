package tools

import (
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/sandbox"
)

func TestBashReportsStdoutAndExitCode(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "echo hello",
	})
	if !strings.Contains(out, "exit_code=0") {
		t.Fatalf("exit code missing: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("stdout missing: %q", out)
	}
}

func TestBashTreatsNonZeroExitAsResult(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "exit 3",
	})
	if !strings.Contains(out, "exit_code=3") {
		t.Fatalf("non-zero exit was not reported as a normal result: %q", out)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatal("non-zero exit was reported as a tool error")
	}
}

func TestBashPutsStderrBeforeStdout(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "echo to-stdout; echo to-stderr >&2",
	})
	errIdx := strings.Index(out, "--- stderr ---")
	outIdx := strings.Index(out, "--- stdout ---")
	if errIdx < 0 || outIdx < 0 {
		t.Fatalf("both streams should be present: %q", out)
	}
	if errIdx > outIdx {
		t.Fatal("stderr is printed after stdout, burying the cause of a failure")
	}
}

func TestBashRefusesRepeatOfFailedCommand(t *testing.T) {
	root := t.TempDir()
	tool := NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()})
	args := map[string]any{"command": "exit 1"}

	if out := run(t, tool, args); !strings.Contains(out, "exit_code=1") {
		t.Fatalf("first run did not fail as expected: %q", out)
	}
	out := run(t, tool, args)
	if !strings.Contains(out, "already failed") {
		t.Fatalf("identical failing command was retried: %q", out)
	}
}

func TestBashAllowsRepeatOfSucceedingCommand(t *testing.T) {
	root := t.TempDir()
	tool := NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()})
	args := map[string]any{"command": "true"}
	run(t, tool, args)
	if out := run(t, tool, args); strings.Contains(out, "already failed") {
		t.Fatalf("a succeeding command was blocked on repeat: %q", out)
	}
}

func TestBashHonorsDenial(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{
		Root: root, Sandbox: sandbox.NewDirect(), Approver: DenyAll{},
	}), map[string]any{"command": "echo should-not-run"})
	if !strings.Contains(out, "read-only") {
		t.Fatalf("denial was not reported: %q", out)
	}
	// The refusal echoes the command, so look for evidence of execution
	// rather than for the command text itself.
	if strings.Contains(out, "exit_code=") {
		t.Fatalf("the command ran despite the denial: %q", out)
	}
}

func TestBashRefusesCwdOutsideRoot(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "pwd", "cwd": "../..",
	})
	if !strings.Contains(out, "outside the workspace") {
		t.Fatalf("cwd escape was not refused: %q", out)
	}
}

func TestBashInterpretsShellFeatures(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "echo one && echo two | tr a-z A-Z",
	})
	if !strings.Contains(out, "TWO") {
		t.Fatalf("pipes or && were not interpreted: %q", out)
	}
}

func TestBashRunsInWorkspaceRootByDefault(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "marker.txt", "x")
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "ls",
	})
	if !strings.Contains(out, "marker.txt") {
		t.Fatalf("command did not run in the workspace root: %q", out)
	}
}

func TestBashReportsTimeout(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{Root: root, Sandbox: sandbox.NewDirect()}), map[string]any{
		"command": "sleep 5", "timeout": 1,
	})
	if !strings.Contains(out, "killed after") {
		t.Fatalf("timeout was not reported: %q", out)
	}
}

func TestBashTruncatesLargeOutput(t *testing.T) {
	root := t.TempDir()
	out := run(t, NewBash(BashConfig{
		Root: root, Sandbox: sandbox.NewDirect(), MaxOutputBytes: 500,
	}), map[string]any{"command": "seq 1 20000"})
	if len(out) > 1200 {
		t.Fatalf("output is %d bytes despite a 500-byte cap", len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("large output was cut without saying so")
	}
	if !strings.Contains(out, "20000") {
		t.Fatal("the tail was dropped, but that is where a failure's verdict lives")
	}
}
