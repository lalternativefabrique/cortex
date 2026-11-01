package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNotConfined is returned when a Container sandbox is asked for outside
// a container: there is no boundary for it to stand on.
var ErrNotConfined = errors.New("sandbox: this process is not in a container; set CORTEX_CONFINED=1 if its host is the boundary")

// Container runs commands in this process's own container, with no further
// confinement: for a service that is itself deployed confined, such as a
// core running its agents' tasks, where the container is the boundary. It is
// never a default, and NewContainer refuses to build one outside a container.
type Container struct {
	// Shell is the interpreter, defaulting to /bin/sh.
	Shell string
	// DefaultTimeout applies when Command.Timeout is zero.
	DefaultTimeout time.Duration
	// Inherit lists the environment names a command keeps; nil keeps
	// DefaultInherit, never the whole environment.
	Inherit []string
	// Passthrough adds names the operator chose to hand to commands.
	Passthrough []string
}

// NewContainer returns a Container sandbox, or ErrNotConfined when this
// process does not run in one.
func NewContainer() (*Container, error) {
	if !confined() {
		return nil, ErrNotConfined
	}
	return &Container{Shell: "/bin/sh", DefaultTimeout: defaultCommandTimeout, Inherit: DefaultInherit}, nil
}

// confined reports a process in a container: a container runtime's marker
// file, a container cgroup, or the operator's word.
func confined() bool {
	if os.Getenv("CORTEX_CONFINED") == "1" {
		return true
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	cgroup, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	for _, runtime := range []string{"docker", "containerd", "kubepods", "libpod", "crio", "sklp", "runc"} {
		if strings.Contains(string(cgroup), runtime) {
			return true
		}
	}
	return false
}

func (c *Container) Name() string { return "container" }

func (c *Container) Run(ctx context.Context, cmd Command) (Output, error) {
	shell := c.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	timeout := cmd.Timeout
	if timeout == 0 {
		timeout = c.DefaultTimeout
	}
	if timeout == 0 {
		timeout = defaultCommandTimeout
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	proc := exec.CommandContext(cctx, shell, "-c", cmd.Line)
	proc.Dir = cmd.Dir
	proc.Stdin = cmd.Stdin
	proc.Env = append(c.environ(), cmd.Env...)
	return execute(cctx, proc, cmd, started)
}

func (c *Container) environ() []string {
	inherit := c.Inherit
	if inherit == nil {
		inherit = DefaultInherit
	}
	return Inherited(environ(), inherit, c.Passthrough)
}
