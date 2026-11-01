package sandbox

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lalternativefabrique/cortex/shell"
)

// Routed runs everything in Space except the commands Host lists, which
// run on this machine: what needs the person's own login, such as a tool
// reading their account. A host command is one plain stage whose words
// start with a listed prefix; it runs without a shell, as those words, so
// nothing rides along with it.
type Routed struct {
	Space Sandbox
	Host  []string
	// Env is the environment a host command gets; nil takes DefaultInherit.
	Env            []string
	DefaultTimeout time.Duration
}

func (r *Routed) Name() string { return r.Space.Name() }

func (r *Routed) Run(ctx context.Context, c Command) (Output, error) {
	words, ok := r.hostCommand(c.Line)
	if !ok {
		return r.Space.Run(ctx, c)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = r.DefaultTimeout
	}
	if timeout == 0 {
		timeout = defaultCommandTimeout
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, words[0], words[1:]...)
	cmd.Dir = c.Dir
	cmd.Stdin = c.Stdin
	cmd.Env = append(r.environ(), c.Env...)
	return execute(cctx, cmd, c, started)
}

func (r *Routed) Close() error {
	if closer, ok := r.Space.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func (r *Routed) environ() []string {
	if r.Env != nil {
		return r.Env
	}
	return Inherited(environ(), DefaultInherit, nil)
}

// hostCommand reports a line that is one listed command and nothing else:
// no chaining, no redirection, no glob, no quoting of the command name.
func (r *Routed) hostCommand(line string) ([]string, bool) {
	stages, err := shell.Parse(line)
	if err != nil || len(stages) != 1 {
		return nil, false
	}
	st := stages[0]
	if len(st.Writes) > 0 || len(st.Reads) > 0 || st.Command().Name() != st.Name() {
		return nil, false
	}
	for _, g := range st.Globs {
		if g {
			return nil, false
		}
	}
	joined := st.String()
	for _, prefix := range r.Host {
		prefix = strings.TrimSpace(prefix)
		if prefix != "" && (joined == prefix || strings.HasPrefix(joined, prefix+" ")) {
			return st.Words, true
		}
	}
	return nil, false
}

// Describe names where a command runs, for a log line.
func (r *Routed) Describe(line string) string {
	if _, ok := r.hostCommand(line); ok {
		return "this machine"
	}
	return fmt.Sprintf("the %s space", r.Space.Name())
}
