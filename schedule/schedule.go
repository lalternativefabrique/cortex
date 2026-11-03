// Package schedule runs an agent on its own: a task is a prompt asked at
// the times a cron expression names, in a conversation of its own, so what
// one run learned the next one continues. The person writes tasks in a
// file or asks the agent to, through schedule_task.
package schedule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// Task is one thing the agent does unprompted.
type Task struct {
	// Name identifies the task; the conversation it runs in is named after it.
	Name string `yaml:"name" json:"name"`
	// Cron is when: five fields, or @hourly, @daily, @weekly, @every 2h.
	Cron   string `yaml:"cron" json:"cron"`
	Prompt string `yaml:"prompt" json:"prompt"`
	// Paused keeps the task without running it.
	Paused bool `yaml:"paused,omitempty" json:"paused,omitempty"`
}

// Conversation is the conversation a task's runs share.
func (t Task) Conversation() string { return "task:" + t.Name }

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// Valid checks the task can be scheduled.
func (t Task) Valid() error {
	if strings.TrimSpace(t.Name) == "" {
		return errors.New("a task needs a name")
	}
	if strings.ContainsAny(t.Name, "/\\\x00") {
		return fmt.Errorf("task name %q cannot contain a slash", t.Name)
	}
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("task %q needs a prompt", t.Name)
	}
	if _, err := parser.Parse(t.Cron); err != nil {
		return fmt.Errorf("task %q: %q is not a schedule: %w", t.Name, t.Cron, err)
	}
	return nil
}

// Next is when the task runs after now, in loc.
func (t Task) Next(now time.Time, loc *time.Location) (time.Time, error) {
	s, err := parser.Parse(t.Cron)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(now.In(loc)), nil
}

type Store interface {
	List(ctx context.Context) ([]Task, error)
	// Put adds the task, or replaces the one of the same name.
	Put(ctx context.Context, t Task) error
	Remove(ctx context.Context, name string) error
}

// DefaultPath is the tasks file, under the person's config directory beside
// their skills, so it is theirs to edit.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "skode", "schedule.yaml"), nil
}

// FileStore keeps the tasks in one YAML file, a list of tasks, read on
// every call so an edit by hand is seen at once.
type FileStore struct {
	mu   sync.Mutex
	path string
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

func (s *FileStore) List(context.Context) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *FileStore) Put(_ context.Context, t Task) error {
	if err := t.Valid(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.read()
	if err != nil {
		return err
	}
	replaced := false
	for i := range tasks {
		if tasks[i].Name == t.Name {
			tasks[i], replaced = t, true
		}
	}
	if !replaced {
		tasks = append(tasks, t)
	}
	return s.write(tasks)
}

func (s *FileStore) Remove(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks, err := s.read()
	if err != nil {
		return err
	}
	kept := tasks[:0]
	for _, t := range tasks {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(tasks) {
		return fmt.Errorf("no task named %q", name)
	}
	return s.write(kept)
}

func (s *FileStore) read() ([]Task, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: read %s: %w", s.path, err)
	}
	var tasks []Task
	if err := yaml.Unmarshal(raw, &tasks); err != nil {
		return nil, fmt.Errorf("schedule: %s: %w", s.path, err)
	}
	return tasks, nil
}

func (s *FileStore) write(tasks []Task) error {
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	raw, err := yaml.Marshal(tasks)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("schedule: create directory: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("schedule: write %s: %w", s.path, err)
	}
	return os.Rename(tmp, s.path)
}
