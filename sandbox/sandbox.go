// Package sandbox isolates how model-decided commands are executed.
//
// The harness depends on the Sandbox interface only. Space, the sklp agent
// space, is the default everywhere a command can run: commands never run on
// the machine that hosts the agent. Container is for a process that is
// itself deployed confined, where that container is the boundary; it must
// be chosen explicitly and refuses to build outside one.
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
