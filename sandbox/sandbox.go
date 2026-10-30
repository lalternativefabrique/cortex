// Package sandbox isolates how model-decided commands are executed.
//
// The harness depends on the Sandbox interface only. Direct execution is the
// right default while the operator writes the prompts and reviews the
// commands: the model can do no more than its operator already could. That
// stops holding the moment the input stops coming from the operator — a
// third-party job, an untrusted repository whose content reaches the model,
// or an unattended trigger with no human gate — and a confining
// implementation becomes required.
package sandbox

import (
	"context"
	"io"
	"time"
)

// Stream classifies a line of subprocess output.
type Stream int

const (
	Stdout Stream = iota
	Stderr
)

// LineFunc receives every full line as it is produced.
//
// stdout and stderr are drained concurrently, so implementations must be
// safe for concurrent use. It must also return quickly: a slow callback
// blocks the subprocess pipes.
type LineFunc func(stream Stream, line string)

// Command is a request to execute a shell command line.
type Command struct {
	// Line is passed to a shell, so pipes, redirections and variable
	// expansion apply.
	Line    string
	Dir     string
	Env     []string
	Timeout time.Duration
	Stdin   io.Reader
	OnLine  LineFunc
}

// Output is the result of an execution.
type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
	// TimedOut reports that the command was killed on Timeout rather than
	// exiting on its own.
	TimedOut bool
	Duration time.Duration
}

// Sandbox executes commands under an isolation policy.
//
// Run reserves its error return for failures to execute at all. A command
// that runs and exits non-zero is a successful Run with a non-zero ExitCode.
type Sandbox interface {
	Run(ctx context.Context, cmd Command) (Output, error)
	// Name identifies the isolation in effect, for display and audit.
	Name() string
}
