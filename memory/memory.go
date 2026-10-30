// Package memory persists what SummaryCompactor extracts from a run —
// decisions and constraints — so they survive past the session that
// produced them and are available to the next one on the same project.
//
// A session's own memory lives in the session's JSONL and dies with it once
// compacted past; this package is the layer above that: one file per
// project, appended to by every session on it, consolidated and injected
// into the next run's stable prefix.
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

// ProjectKey identifies a project across clones and worktrees, so a feature
// branch checked out in its own sklp flow worktree still resolves to the
// same memory as the main checkout.
//
// It is the repository's origin remote URL when one is configured — stable
// across every clone and worktree of the same repository — falling back to
// the first commit's hash for a repository with no remote, and finally to
// root itself for a directory that is not a git repository at all.
func ProjectKey(root string) string {
	if url, err := git(root, "remote", "get-url", "origin"); err == nil && url != "" {
		return url
	}
	if hash, err := git(root, "rev-list", "--max-parents=0", "HEAD"); err == nil && hash != "" {
		return strings.SplitN(hash, "\n", 2)[0]
	}
	return root
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Dir returns the directory project memories live in, creating it if
// needed. It follows XDG, alongside session.Dir.
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "skode", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create memory directory: %w", err)
	}
	return dir, nil
}

// path turns a project key into the file it is stored under. The key is
// hashed rather than used as a filename directly: a remote URL contains `/`
// and `:`, neither of which survives as a path component.
func path(dir, key string) string {
	h := fnv.New32a()
	h.Write([]byte(key))
	return filepath.Join(dir, fmt.Sprintf("%08x.jsonl", h.Sum32()))
}

// entry is one extraction, appended as a line.
type entry struct {
	At      time.Time              `json:"at"`
	Extract agent.MemoryExtraction `json:"extract"`
}

// Store appends extractions for one project.
type Store struct {
	key string
	dir string
}

// NewStore opens the store for the project at root.
func NewStore(root string) (*Store, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return &Store{key: ProjectKey(root), dir: dir}, nil
}

// Append implements agent.MemoryRecorder.
func (s *Store) Append(mem agent.MemoryExtraction) error {
	f, err := os.OpenFile(path(s.dir, s.key), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open project memory: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(entry{At: time.Now(), Extract: mem})
	if err != nil {
		return fmt.Errorf("encode project memory: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write project memory: %w", err)
	}
	return nil
}

// DefaultMaxItems bounds how many decisions or constraints Load keeps, most
// recent first — an unbounded project memory would eventually cost more
// tokens on every run than the re-explaining it exists to avoid.
const DefaultMaxItems = 20

// Load reads and consolidates every extraction stored for the project at
// root, most recent first, deduplicated and capped at maxItems per section.
// Zero maxItems means DefaultMaxItems.
func Load(root string, maxItems int) (agent.MemoryExtraction, error) {
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	dir, err := Dir()
	if err != nil {
		return agent.MemoryExtraction{}, err
	}
	key := ProjectKey(root)

	f, err := os.Open(path(dir, key))
	if err != nil {
		if os.IsNotExist(err) {
			return agent.MemoryExtraction{}, nil
		}
		return agent.MemoryExtraction{}, fmt.Errorf("open project memory: %w", err)
	}
	defer f.Close()

	var entries []entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var e entry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return agent.MemoryExtraction{}, fmt.Errorf("read project memory: %w", err)
	}

	// Newest first, so capping at maxItems keeps what was most recently
	// established rather than the oldest, possibly superseded, entries.
	decisions := dedupLastN(entriesReversed(entries, func(e entry) []string { return e.Extract.Decisions }), maxItems)
	constraints := dedupLastN(entriesReversed(entries, func(e entry) []string { return e.Extract.Constraints }), maxItems)
	return agent.MemoryExtraction{Decisions: decisions, Constraints: constraints}, nil
}

func entriesReversed(entries []entry, field func(entry) []string) []string {
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		out = append(out, field(entries[i])...)
	}
	return out
}

// dedupLastN keeps the first maxItems unique items from items, which are
// expected newest-first, so a repeated decision keeps its most recent
// position rather than its oldest.
func dedupLastN(items []string, maxItems int) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, maxItems)
	for _, item := range items {
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
		if len(out) == maxItems {
			break
		}
	}
	return out
}
