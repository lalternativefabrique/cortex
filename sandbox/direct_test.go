package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunCapturesStdoutAndExitCode(t *testing.T) {
	out, err := NewDirect().Run(context.Background(), Command{Line: "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.Stdout) != "hello" {
		t.Fatalf("Stdout = %q", out.Stdout)
	}
	if out.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", out.ExitCode)
	}
}

func TestRunSeparatesStderr(t *testing.T) {
	out, err := NewDirect().Run(context.Background(), Command{Line: "echo out; echo err >&2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.Stdout) != "out" || strings.TrimSpace(out.Stderr) != "err" {
		t.Fatalf("streams crossed: stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
}

func TestRunReportsNonZeroExitWithoutError(t *testing.T) {
	out, err := NewDirect().Run(context.Background(), Command{Line: "exit 7"})
	if err != nil {
		t.Fatalf("a command that ran and failed returned an error: %v", err)
	}
	if out.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", out.ExitCode)
	}
}

func TestRunInterpretsShellFeatures(t *testing.T) {
	out, err := NewDirect().Run(context.Background(), Command{
		Line: "echo one && echo two | tr a-z A-Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Stdout, "TWO") {
		t.Fatalf("pipes or && were not interpreted: %q", out.Stdout)
	}
}

func TestRunHonorsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644)

	out, err := NewDirect().Run(context.Background(), Command{Line: "ls", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Stdout, "marker") {
		t.Fatalf("command did not run in Dir: %q", out.Stdout)
	}
}

func TestRunStreamsLinesAsProduced(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	_, err := NewDirect().Run(context.Background(), Command{
		Line: "echo a; echo b >&2; echo c",
		OnLine: func(_ Stream, line string) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, line)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d streamed lines, want 3: %v", len(lines), lines)
	}
}

func TestRunClassifiesStreamedLines(t *testing.T) {
	var mu sync.Mutex
	seen := map[Stream][]string{}
	_, err := NewDirect().Run(context.Background(), Command{
		Line: "echo out; echo err >&2",
		OnLine: func(s Stream, line string) {
			mu.Lock()
			defer mu.Unlock()
			seen[s] = append(seen[s], line)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen[Stdout]) != 1 || len(seen[Stderr]) != 1 {
		t.Fatalf("streams misclassified: %+v", seen)
	}
}

func TestRunKillsOnTimeout(t *testing.T) {
	started := time.Now()
	out, err := NewDirect().Run(context.Background(), Command{
		Line:    "sleep 10",
		Timeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.TimedOut {
		t.Fatal("TimedOut = false for a command killed on its deadline")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s to take effect", elapsed)
	}
}

func TestRunKillsChildProcessesOnTimeout(t *testing.T) {
	// A shell that spawns a child and exits leaves the child holding the
	// pipes; killing only the shell would hang the read until the child
	// finishes on its own.
	started := time.Now()
	_, err := NewDirect().Run(context.Background(), Command{
		Line:    "sleep 30 & wait",
		Timeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("the child outlived the timeout: run took %s", elapsed)
	}
}

func TestRunHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	if _, err := NewDirect().Run(ctx, Command{Line: "sleep 30", Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("cancellation was ignored: run took %s", elapsed)
	}
}

func TestRunPassesEnvironment(t *testing.T) {
	out, err := NewDirect().Run(context.Background(), Command{
		Line: "echo $AI_TEST_VAR",
		Env:  []string{"AI_TEST_VAR=present"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.Stdout) != "present" {
		t.Fatalf("Env was not applied: %q", out.Stdout)
	}
}

func TestRunHandlesLongLines(t *testing.T) {
	// Compiler and test output routinely exceeds the scanner's default
	// buffer; a line longer than it would otherwise abort the capture.
	out, err := NewDirect().Run(context.Background(), Command{
		Line: "head -c 200000 /dev/zero | tr '\\0' 'x'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Stdout) < 200000 {
		t.Fatalf("captured %d bytes of a 200000-byte line", len(out.Stdout))
	}
}

func TestNameReportsIsolation(t *testing.T) {
	if got := NewDirect().Name(); got != "none" {
		t.Fatalf("Name = %q, want %q so the operator can see what confinement is in effect", got, "none")
	}
}
