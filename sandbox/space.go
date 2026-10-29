package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SpaceWorkdir is where the workspace is mounted inside the space.
const SpaceWorkdir = "/workspace"

// Space runs every command inside the workspace's sklp agent space
// (`sklp agent up`): the CI runner's environment from .sklp/space.yaml,
// held open for the session, that sees the workspace and nothing else of
// the machine — no home directory, no other process, no loopback of the
// host, no credential in its environment, its secret files mounted empty.
type Space struct {
	// Sklp is the sklp binary; it needs "agent up".
	Sklp string
	// Root is the host workspace, mounted read-write at SpaceWorkdir.
	Root string
	// Hidden are extra files under Root to mount empty, beyond those sklp
	// recognises as secret by name.
	Hidden []string
	// Mounts are extra "host:space[:ro]" binds.
	Mounts []string
	// StartTimeout bounds how long the first command waits for the space,
	// the first build of the workspace's recipe included.
	StartTimeout   time.Duration
	DefaultTimeout time.Duration

	once    sync.Once
	name    string
	up      *exec.Cmd
	upIn    io.WriteCloser
	started error
}

func (s *Space) Name() string { return "sklp-agent-space" }

func (s *Space) Run(ctx context.Context, c Command) (Output, error) {
	if err := s.start(ctx); err != nil {
		return Output{}, err
	}
	dir, err := s.inside(c.Dir)
	if err != nil {
		return Output{}, err
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = s.DefaultTimeout
	}
	if timeout == 0 {
		timeout = defaultCommandTimeout
	}
	started := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"space", "exec", s.name, "--", "env"}
	args = append(args, c.Env...)
	args = append(args, "sh", "-c", "cd "+shellQuote(dir)+" && "+c.Line)
	cmd := exec.CommandContext(cctx, s.Sklp, args...)
	cmd.Stdin = c.Stdin
	return execute(cctx, cmd, c, started)
}

// Warm opens the space ahead of the first command, so building the
// workspace's recipe does not stall it.
func (s *Space) Warm(ctx context.Context) {
	go func() { _ = s.start(ctx) }()
}

// Close ends the space with the agent: `sklp agent up` tears it down when
// its input closes.
func (s *Space) Close() error {
	if s.upIn == nil {
		return nil
	}
	_ = s.upIn.Close()
	time.AfterFunc(30*time.Second, func() { _ = s.up.Process.Kill() })
	return nil
}

// inside maps a host directory under Root to its path in the space.
func (s *Space) inside(dir string) (string, error) {
	if dir == "" {
		return SpaceWorkdir, nil
	}
	rel, err := filepath.Rel(s.Root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: %s is outside the workspace the space sees", dir)
	}
	return filepath.Join(SpaceWorkdir, rel), nil
}

// start opens the space once and waits until a command runs in it. If it
// cannot start, every command fails: falling back to the host would drop
// the very boundary the space is for.
func (s *Space) start(ctx context.Context) error {
	s.once.Do(func() { s.started = s.open(ctx) })
	return s.started
}

func (s *Space) open(ctx context.Context) error {
	args := []string{"agent", "up", "-repo", s.Root}
	for _, p := range s.Hidden {
		args = append(args, "-hide", p)
	}
	for _, m := range s.Mounts {
		args = append(args, "-mount", m)
	}
	up := exec.Command(s.Sklp, args...)
	in, err := up.StdinPipe()
	if err != nil {
		return fmt.Errorf("sandbox: agent space input: %w", err)
	}
	out, err := up.StdoutPipe()
	if err != nil {
		return fmt.Errorf("sandbox: agent space output: %w", err)
	}
	var said strings.Builder
	up.Stderr = &lockedWriter{w: &said}
	if err := up.Start(); err != nil {
		return fmt.Errorf("sandbox: start agent space: %w", err)
	}
	s.up, s.upIn = up, in

	// The first line names the space; what follows is not needed.
	first, err := bufio.NewReader(out).ReadString('\n')
	go func() { _, _ = io.Copy(io.Discard, out) }()
	name, ok := strings.CutPrefix(strings.TrimSpace(first), "agent space name=")
	if err != nil || !ok || name == "" {
		return fmt.Errorf("sandbox: sklp agent up did not name its space: %q %s", first, strings.TrimSpace(said.String()))
	}
	s.name = name

	exited := make(chan error, 1)
	go func() { exited <- up.Wait() }()
	limit := s.StartTimeout
	if limit == 0 {
		limit = 15 * time.Minute
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if s.ready(ctx) {
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("sandbox: agent space %s stopped while starting: %s", name, lastLines(said.String(), 5))
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("sandbox: agent space %s did not start within %s", name, limit)
}

func (s *Space) ready(ctx context.Context) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, s.Sklp, "space", "exec", s.name, "--", "true").Run() == nil
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
