package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

const defaultCommandTimeout = 2 * time.Minute

// execute runs cmd to completion, streaming its output to c.OnLine and
// killing its whole process group when cctx ends.
func execute(cctx context.Context, cmd *exec.Cmd, c Command, started time.Time) (Output, error) {
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

// MaxCaptureBytes bounds what one stream of a command keeps in memory;
// past it the output is still read, so the command is not blocked, and
// dropped.
const MaxCaptureBytes = 8 << 20

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
		if capture.Len() < MaxCaptureBytes {
			capture.WriteString(line)
			capture.WriteByte('\n')
		}
		if onLine != nil {
			onLine(stream, line)
		}
	}
}
