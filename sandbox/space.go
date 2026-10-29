package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SpaceWorkdir is where the workspace is mounted inside the space.
const SpaceWorkdir = "/workspace"

// Space runs every command inside a sklp space: a rootless container that
// sees the workspace and nothing else of the machine — no home directory,
// no other process, no loopback of the host, no credential in its
// environment. Files listed in Hidden are mounted empty, so a secret stays
// out of reach whatever command reads it.
type Space struct {
	// Sklp is the sklp binary; it needs "space up -mount".
	Sklp string
	// Root is the host workspace, mounted read-write at SpaceWorkdir.
	Root string
	// Image is the OCI image the space starts from. Empty builds the
	// workspace's own .sklp/space.yaml recipe.
	Image string
	// Hidden are host paths under Root mounted empty and read-only.
	Hidden []string
	// ReadOnly are host paths under Root mounted read-only.
	ReadOnly []string
	// StartTimeout bounds how long the first command waits for the space.
	StartTimeout   time.Duration
	DefaultTimeout time.Duration

	once    sync.Once
	up      *exec.Cmd
	upIn    io.WriteCloser
	started error
}

// SpaceName is the space a workspace gets: stable per directory, so a
// restarted agent finds the same one.
func SpaceName(root string) string {
	sum := sha256.Sum256([]byte(root))
	return "ark-" + strings.ToLower(filepath.Base(root)) + "-" + hex.EncodeToString(sum[:])[:8]
}

func (s *Space) Name() string { return "sklp-space" }

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

	args := []string{"space", "exec", SpaceName(s.Root), "--", "env"}
	args = append(args, c.Env...)
	args = append(args, "sh", "-c", "cd "+shellQuote(dir)+" && "+c.Line)
	cmd := exec.CommandContext(cctx, s.Sklp, args...)
	cmd.Stdin = c.Stdin
	return execute(cctx, cmd, c, started)
}

// Close ends the space with the agent: its shell exits when its input
// closes, and sklp stops the space behind it.
func (s *Space) Close() error {
	if s.upIn == nil {
		return nil
	}
	_ = s.upIn.Close()
	time.AfterFunc(10*time.Second, func() { _ = s.up.Process.Kill() })
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

// start opens the space once, in the foreground of a sklp process kept alive
// by an open input, and waits until a command runs in it. If it cannot
// start, every command fails: falling back to the host would drop the very
// boundary the space is for.
func (s *Space) start(ctx context.Context) error {
	s.once.Do(func() { s.started = s.open(ctx) })
	return s.started
}

func (s *Space) open(ctx context.Context) error {
	name := SpaceName(s.Root)
	if s.ready(ctx, name) {
		return nil
	}
	args := []string{"space", "up", "-workdir", SpaceWorkdir, "-mount", s.Root + ":" + SpaceWorkdir}
	for _, p := range s.ReadOnly {
		if in, err := s.inside(p); err == nil {
			args = append(args, "-mount", p+":"+in+":ro")
		}
	}
	for _, p := range s.Hidden {
		if in, err := s.inside(p); err == nil {
			args = append(args, "-mount", os.DevNull+":"+in+":ro")
		}
	}
	if s.Image != "" {
		args = append(args, "-image", s.Image)
	} else {
		args = append(args, "-repo", s.Root)
	}
	args = append(args, name)
	up := exec.Command(s.Sklp, args...)
	in, err := up.StdinPipe()
	if err != nil {
		return fmt.Errorf("sandbox: space input: %w", err)
	}
	var said strings.Builder
	up.Stdout, up.Stderr = io.Discard, &lockedWriter{w: &said}
	if err := up.Start(); err != nil {
		return fmt.Errorf("sandbox: start space %s: %w", name, err)
	}
	s.up, s.upIn = up, in

	limit := s.StartTimeout
	if limit == 0 {
		limit = 10 * time.Minute
	}
	exited := make(chan error, 1)
	go func() { exited <- up.Wait() }()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if s.ready(ctx, name) {
			return nil
		}
		select {
		case <-exited:
			return fmt.Errorf("sandbox: space %s stopped while starting: %s", name, strings.TrimSpace(said.String()))
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("sandbox: space %s did not start within %s", name, limit)
}

func (s *Space) ready(ctx context.Context, name string) bool {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(cctx, s.Sklp, "space", "exec", name, "--", "true").Run() == nil
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
