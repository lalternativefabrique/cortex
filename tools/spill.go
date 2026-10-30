package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// spillDir is where output too large for the context is kept. It sits beside
// the sessions rather than in the workspace, so a long test run does not turn
// up in the diff the operator is about to commit.
func spillDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "skode", "output")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// spill writes s where it can be read later and returns the path.
//
// Truncation throws away the middle of a long output, which is where a test
// suite puts the failure that follows its first one. Keeping the whole thing
// on disk costs nothing and turns "the rest is gone" into a grep away.
//
// The name is derived from the content, so re-running a command that produces
// the same output does not fill the directory with copies of it.
func spill(s string) (string, error) {
	dir, err := spillDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(s))
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".txt")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		return "", err
	}
	pruneSpills(dir, maxSpillFiles)
	return path, nil
}

// maxSpillFiles bounds the directory. Nothing else ever deletes from it, and
// a week of test runs would otherwise leave every output ever truncated.
const maxSpillFiles = 64

// pruneSpills drops the oldest files past the cap, best-effort: failing to
// tidy up must not fail the command that produced the output.
func pruneSpills(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= keep {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	files := make([]aged, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, aged{e.Name(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[min(keep, len(files)):] {
		os.Remove(filepath.Join(dir, f.name))
	}
}

// spillNote tells the model where the untruncated output is, in terms of what
// it can do about it.
func spillNote(path string, total int) string {
	return fmt.Sprintf("[%d bytes in full at %s — read or grep it for what the middle dropped]\n",
		total, path)
}
