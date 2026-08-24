package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Direct runs commands on the host with no confinement, bounded only by a
// timeout. It is the default for an operator driving the agent on their own
// machine and their own repository.
type Direct struct {
	// Shell is the interpreter, defaulting to /bin/sh.
	Shell string
	// DefaultTimeout applies when Command.Timeout is zero.
	DefaultTimeout time.Duration
}

const defaultCommandTimeout = 2 * time.Minute

// NewDirect returns a Direct sandbox with the standard shell and timeout.
func NewDirect() *Direct {
	return &Direct{Shell: "/bin/sh", DefaultTimeout: defaultCommandTimeout}
}

func (d *Direct) Name() string { return "none" }

func (d *Direct) Run(ctx context.Context, c Command) (Output, error) {
	shell := d.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = d.DefaultTimeout
	}
	if timeout == 0 {
		timeout = defaultCommandTimeout
	}

	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, shell, "-c", c.Line)
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	// A shell spawns children; killing only the shell leaves them running
	// and holding the pipes open, so the whole group is signalled instead.
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return Output{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return Output{}, fmt.Errorf("stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return Output{}, fmt.Errorf("start %q: %w", c.Line, err)
	}

	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go drain(&wg, stdoutPipe, &stdout, Stdout, c.OnLine)
	go drain(&wg, stderrPipe, &stderr, Stderr, c.OnLine)
	wg.Wait()

	waitErr := cmd.Wait()
	out := Output{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(started),
		TimedOut: cctx.Err() == context.DeadlineExceeded,
	}
	if waitErr != nil {
		var ee *exec.ExitError
		if ok := asExitError(waitErr, &ee); ok {
			out.ExitCode = ee.ExitCode()
		} else if out.TimedOut {
			out.ExitCode = -1
		} else {
			return out, fmt.Errorf("run %q: %w", c.Line, waitErr)
		}
	}
	return out, nil
}

// drain reads a pipe line by line, capturing every line and forwarding it to
// onLine as it arrives. The buffer is raised well above the default because
// compiler and test output routinely emits very long lines.
func drain(wg *sync.WaitGroup, r io.ReadCloser, capture *bytes.Buffer, stream Stream, onLine LineFunc) {
	defer wg.Done()
	defer r.Close()
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		capture.WriteString(line)
		capture.WriteByte('\n')
		if onLine != nil {
			onLine(stream, line)
		}
	}
}
