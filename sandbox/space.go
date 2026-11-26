package sandbox

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// Sklp is the sklp binary; empty looks it up on PATH. It needs "agent up".
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

	mu     sync.Mutex
	name   string
	up     *exec.Cmd
	upIn   io.WriteCloser
	exited chan struct{}
	// joined is a space another agent on the same workspace opened and
	// holds: this one runs its commands there and never tears it down.
	joined  bool
	checked time.Time
}

// joinedRecheck is how long a joined space is trusted before it is checked
// again: its holder may have gone, and the space with it.
const joinedRecheck = 30 * time.Second

// AgentSpaceName is the space sklp gives a workspace's agent
// (sklp space.AgentSpaceName): one per checkout, whichever agent opens it.
func AgentSpaceName(root string) string {
	sum := sha256.Sum256([]byte(root))
	base := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.ToLower(filepath.Base(root)))
	if base == "" {
		base = "run"
	}
	return "agent-" + base + "-" + hex.EncodeToString(sum[:4])
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
	cmd := s.command(cctx, args...)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.joined || s.upIn == nil {
		return nil
	}
	_ = s.upIn.Close()
	time.AfterFunc(30*time.Second, func() { _ = s.up.Process.Kill() })
	return nil
}

// inside maps a host directory under Root, or under one of Mounts, to its
// path in the space.
func (s *Space) inside(dir string) (string, error) {
	if dir == "" {
		return SpaceWorkdir, nil
	}
	if rel, ok := under(s.Root, dir); ok {
		return filepath.Join(SpaceWorkdir, rel), nil
	}
	for _, m := range s.Mounts {
		parts := strings.Split(m, ":")
		if len(parts) < 2 {
			continue
		}
		if rel, ok := under(parts[0], dir); ok {
			return filepath.Join(parts[1], rel), nil
		}
	}
	return "", fmt.Errorf("sandbox: %s is outside the workspace the space sees", dir)
}

func under(base, dir string) (string, bool) {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// start opens the space, or reopens it when it stopped, and waits until a
// command runs in it. If it cannot start, the command fails: falling back to
// the host would drop the very boundary the space is for.
//
// Another agent on the same workspace may already hold the space: it is
// joined rather than opened a second time, which sklp refuses with a busy
// overlay. A space left mounted by a holder that died is stopped, keeping
// its data, and opened again.
func (s *Space) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.joined {
		if time.Since(s.checked) < joinedRecheck {
			return nil
		}
		if s.ready(ctx) {
			s.checked = time.Now()
			return nil
		}
		s.joined = false
	}
	if s.exited != nil {
		select {
		case <-s.exited:
		default:
			return nil
		}
	}
	s.name = AgentSpaceName(s.Root)
	if s.ready(ctx) {
		s.joined, s.checked = true, time.Now()
		return nil
	}
	err := s.openOnce(ctx)
	if err != nil && strings.Contains(err.Error(), "resource busy") {
		stop := s.command(ctx, "space", "stop", s.name)
		if out, serr := stop.CombinedOutput(); serr != nil {
			return fmt.Errorf("%w; freeing it: sklp space stop %s: %s", err, s.name, strings.TrimSpace(string(out)))
		}
		err = s.openOnce(ctx)
	}
	return err
}

func (s *Space) openOnce(ctx context.Context) error {
	err := s.open(ctx)
	if err != nil && s.up != nil && s.up.Process != nil {
		_ = s.upIn.Close()
		_ = s.up.Process.Kill()
	}
	return err
}

func (s *Space) open(ctx context.Context) error {
	args := []string{"agent", "up", "-repo", s.Root}
	for _, p := range s.Hidden {
		args = append(args, "-hide", p)
	}
	for _, m := range s.Mounts {
		args = append(args, "-mount", m)
	}
	up := s.command(context.Background(), args...)
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
	exited := make(chan struct{})
	s.up, s.upIn, s.exited = up, in, exited
	go func() { _ = up.Wait(); close(exited) }()

	// The first line names the space; what follows is not needed.
	first, err := bufio.NewReader(out).ReadString('\n')
	go func() { _, _ = io.Copy(io.Discard, out) }()
	name, ok := strings.CutPrefix(strings.TrimSpace(first), "agent space name=")
	if err != nil || !ok || name == "" {
		return fmt.Errorf("sandbox: sklp agent up did not name its space: %q %s", first, strings.TrimSpace(said.String()))
	}
	s.name = name

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
	return s.command(cctx, "space", "exec", s.name, "--", "true").Run() == nil
}

// command runs sklp from the workspace, not from the agent's own working
// directory: an app started from the Finder runs in /, and on a Mac sklp
// relays into its VM only from under the home directory.
func (s *Space) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, s.sklp(), args...)
	cmd.Dir = s.Root
	return cmd
}

func (s *Space) sklp() string {
	if s.Sklp == "" {
		return "sklp"
	}
	return s.Sklp
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
