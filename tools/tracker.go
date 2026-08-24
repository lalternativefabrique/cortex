package tools

import (
	"sync"
	"time"
)

// ReadTracker remembers which files the agent has read, and at which
// modification time.
//
// The edit and write tools consult it to refuse changing a file the model
// has not looked at, and to detect a file that changed underneath the agent
// since it was read — an edit computed against stale content silently
// corrupts the file.
type ReadTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// NewReadTracker returns an empty tracker.
func NewReadTracker() *ReadTracker {
	return &ReadTracker{seen: map[string]time.Time{}}
}

// MarkRead records that path was read while it had the given mtime.
func (t *ReadTracker) MarkRead(path string, mod time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[path] = mod
}

// ReadAt returns the mtime the file had when it was read, and whether it was
// read at all.
func (t *ReadTracker) ReadAt(path string) (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	mod, ok := t.seen[path]
	return mod, ok
}

// Forget drops a path, used after a write so the next edit re-reads it.
func (t *ReadTracker) Forget(path string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.seen, path)
}
