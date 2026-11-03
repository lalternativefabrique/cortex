package profile

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Dir returns the directory profiles live in, creating it if needed. It
// follows XDG, alongside memory.Dir.
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "skode", "profile")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create profile directory: %w", err)
	}
	return dir, nil
}

// FileStore keeps profiles on disk, one directory per subject: signals as
// one JSONL, facts as another where a retraction is a line of its own, so
// nothing written is ever rewritten.
type FileStore struct {
	mu  sync.Mutex
	dir string
}

// NewFileStore opens the store under Dir.
func NewFileStore() (*FileStore, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return NewFileStoreAt(dir), nil
}

// NewFileStoreAt opens the store under dir.
func NewFileStoreAt(dir string) *FileStore {
	return &FileStore{dir: dir}
}

func (s *FileStore) subjectDir(scope Scope) string {
	h := fnv.New32a()
	h.Write([]byte(scope.Subject))
	return filepath.Join(s.dir, fmt.Sprintf("%08x", h.Sum32()))
}

type factLine struct {
	Fact    *Fact      `json:"fact,omitempty"`
	Retract string     `json:"retract,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

func (s *FileStore) Observe(_ context.Context, sig Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendLine(filepath.Join(s.subjectDir(sig.Scope), "signals.jsonl"), sig)
}

func (s *FileStore) Signals(_ context.Context, scope Scope, since time.Time, limit int) ([]Signal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []Signal
	err := readLines(filepath.Join(s.subjectDir(scope), "signals.jsonl"), func(raw []byte) {
		var sig Signal
		if json.Unmarshal(raw, &sig) == nil && sig.Scope == scope && !sig.At.Before(since) {
			all = append(all, sig)
		}
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].At.After(all[j].At) })
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (s *FileStore) Facts(_ context.Context, scope Scope) ([]Fact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	facts, err := s.replay(scope)
	if err != nil {
		return nil, err
	}
	var valid []Fact
	for _, f := range facts {
		if f.Valid() {
			valid = append(valid, f)
		}
	}
	sort.SliceStable(valid, func(i, j int) bool { return valid[i].Since.After(valid[j].Since) })
	return valid, nil
}

func (s *FileStore) replay(scope Scope) ([]Fact, error) {
	var order []string
	byID := map[string]*Fact{}
	err := readLines(filepath.Join(s.subjectDir(scope), "facts.jsonl"), func(raw []byte) {
		var line factLine
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		switch {
		case line.Fact != nil && line.Fact.Scope == scope:
			f := *line.Fact
			if _, seen := byID[f.ID]; !seen {
				order = append(order, f.ID)
			}
			byID[f.ID] = &f
		case line.Retract != "":
			if f, ok := byID[line.Retract]; ok && f.Until == nil {
				f.Until = line.At
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]Fact, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

func (s *FileStore) Assert(_ context.Context, f Fact) error {
	if f.ID == "" {
		return errors.New("profile: a fact needs an id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendLine(filepath.Join(s.subjectDir(f.Scope), "facts.jsonl"), factLine{Fact: &f})
}

func (s *FileStore) Retract(_ context.Context, scope Scope, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return appendLine(filepath.Join(s.subjectDir(scope), "facts.jsonl"), factLine{Retract: id, At: &at})
}

func (s *FileStore) Forget(_ context.Context, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.subjectDir(scope))
}

func appendLine(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("profile: create directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("profile: open: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("profile: encode: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("profile: write: %w", err)
	}
	return nil
}

func readLines(path string, each func([]byte)) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("profile: open: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		each(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("profile: read: %w", err)
	}
	return nil
}
