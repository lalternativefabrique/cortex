// Package session persists a run's conversation so it can be resumed.
//
// The store is a JSONL file per session: one message per line, appended as
// the run proceeds. That shape survives a crash mid-write — the last line is
// lost, not the file — and stays greppable, which a binary format or a
// database would not be for something the operator may want to read.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Record is one line of a session file.
//
// The header line carries the metadata; every later line carries a message.
type Record struct {
	Type    string         `json:"type"`
	Root    string         `json:"root,omitempty"`
	Model   string         `json:"model,omitempty"`
	Started string         `json:"started,omitempty"`
	Prompt  string         `json:"prompt,omitempty"`
	Message *agent.Message `json:"message,omitempty"`
	Usage   *agent.Usage   `json:"usage,omitempty"`
	// At timestamps a usage record, so consumption can be attributed to a
	// day even when a session spans midnight.
	At string `json:"at,omitempty"`
	// Branch is where the work sat when a turn ran. Written only when it
	// changes, so the journal reads as the moves rather than a repetition.
	Branch string `json:"branch,omitempty"`
}

const (
	recordHeader  = "header"
	recordMessage = "message"
	recordUsage   = "usage"
	// A session outlives the branch it started on: work begun on main moves
	// to a feature branch, and the answer to "what was I doing here" is the
	// branches it passed through, not the one it happened to open on.
	recordBranch = "branch"
)

// Store appends a session to disk.
type Store struct {
	// branch is the last one written, so an unchanged branch is not repeated
	// on every turn.
	branch string
	path   string
	f      *os.File
	w      *bufio.Writer
}

// Dir returns the directory sessions live in, creating it if needed.
//
// It follows XDG so sessions land beside the user's other state rather than
// inside the repository being worked on, where they would show up in git
// status and in the agent's own searches.
func Dir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, "skode", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create session directory: %w", err)
	}
	return dir, nil
}

// Create opens a new session file named after the given time and returns its
// store. The id is the file's basename without extension.
func Create(now time.Time, root, model, prompt string) (*Store, string, error) {
	dir, err := Dir()
	if err != nil {
		return nil, "", err
	}
	id := now.Format("20060102-150405")
	path := filepath.Join(dir, id+".jsonl")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("create session file: %w", err)
	}
	s := &Store{path: path, f: f, w: bufio.NewWriter(f)}
	if err := s.append(Record{
		Type:    recordHeader,
		Root:    root,
		Model:   model,
		Started: now.Format(time.RFC3339),
		Prompt:  prompt,
	}); err != nil {
		f.Close()
		return nil, "", err
	}
	return s, id, nil
}

// Append records one message.
func (s *Store) Append(m agent.Message) error {
	return s.append(Record{Type: recordMessage, Message: &m})
}

// AppendUsage records what a turn consumed.
//
// Kept as its own record rather than folded into the header: a session has
// many turns, and a running total written once at the end is lost when the
// process is interrupted — which is how sessions usually end.
func (s *Store) AppendUsage(u agent.Usage, model string, at time.Time) error {
	return s.append(Record{
		Type:  recordUsage,
		Usage: &u,
		Model: model,
		At:    at.UTC().Format(time.RFC3339),
	})
}

func (s *Store) append(r Record) error {
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode session record: %w", err)
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write session record: %w", err)
	}
	// Flushed per record so an interrupted run still leaves a resumable
	// file: buffering would silently drop the tail on Ctrl-C.
	return s.w.Flush()
}

// Path is the session file's location.
// AppendBranch notes where the work sits, when that has changed since the
// last turn. A session that never leaves its branch writes this once.
func (s *Store) AppendBranch(branch string) error {
	if branch == "" || branch == s.branch {
		return nil
	}
	s.branch = branch
	return s.append(Record{Type: recordBranch, Branch: branch})
}

func (s *Store) Path() string { return s.path }

// Close flushes and closes the file.
func (s *Store) Close() error {
	if err := s.w.Flush(); err != nil {
		s.f.Close()
		return err
	}
	return s.f.Close()
}

// Session is a session read back from disk.
type Session struct {
	ID       string
	Root     string
	Model    string
	Started  time.Time
	Prompt   string
	Messages []agent.Message
	Turns    []Turn
}

// Turn is what one exchange consumed.
type Turn struct {
	Model string
	At    time.Time
	Usage agent.Usage
}

// Usage totals what the whole session consumed.
func (s *Session) Usage() agent.Usage {
	var total agent.Usage
	for _, t := range s.Turns {
		total.Add(t.Usage)
	}
	return total
}

// Load reads a session by id, or by path when id contains a separator.
func Load(id string) (*Session, error) {
	path := id
	if !strings.ContainsRune(id, filepath.Separator) {
		dir, err := Dir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(dir, strings.TrimSuffix(id, ".jsonl")+".jsonl")
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no session %q; list them with -sessions", id)
		}
		return nil, err
	}
	defer f.Close()

	out := &Session{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			// A run killed mid-write leaves a partial final line. Everything
			// before it is still valid, so stop here rather than fail.
			break
		}
		switch r.Type {
		case recordHeader:
			out.Root, out.Model, out.Prompt = r.Root, r.Model, r.Prompt
			if t, err := time.Parse(time.RFC3339, r.Started); err == nil {
				out.Started = t
			}
		case recordMessage:
			if r.Message != nil {
				out.Messages = append(out.Messages, *r.Message)
			}
		case recordUsage:
			if r.Usage != nil {
				turn := Turn{Model: r.Model, Usage: *r.Usage}
				if t, err := time.Parse(time.RFC3339, r.At); err == nil {
					turn.At = t
				}
				out.Turns = append(out.Turns, turn)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return out, nil
}

// Reopen returns a store that appends to an existing session file.
func Reopen(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("reopen session: %w", err)
	}
	return &Store{path: path, f: f, w: bufio.NewWriter(f)}, nil
}

// Summary describes a stored session without loading its messages.
type Summary struct {
	ID      string
	Root    string
	Model   string
	Started time.Time
	Prompt  string
	// Touched are the files the session wrote, which is what tells two
	// sessions on the same subject apart: the question they opened with is
	// often the same, the work rarely is.
	Touched []string
	// Branches are the ones the work passed through, in order. The last is
	// where it left off, which is what a list wants to show.
	Branches []string
}

// List returns stored sessions, most recent first.
func List(limit int) ([]Summary, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []Summary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		s, err := readHeader(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// maxSummaryLines bounds how much of a session is read to describe it. The
// opening question and the first writes are near the top; a long run's tail
// adds nothing a listing can show.
const maxSummaryLines = 400

// readHeader reads the top of a session, so listing does not pay for message
// history it will not show.
func readHeader(path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		return Summary{}, fmt.Errorf("empty session file")
	}
	var r Record
	if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
		return Summary{}, err
	}
	s := Summary{
		ID:     strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Root:   r.Root,
		Model:  r.Model,
		Prompt: r.Prompt,
	}
	if t, err := time.Parse(time.RFC3339, r.Started); err == nil {
		s.Started = t
	}
	scanSummary(scanner, &s)
	return s, nil
}

// scanSummary fills in what the header cannot hold: an interactive session
// writes its header before the first question is asked, so the prompt is in
// the messages, and what the session did is in the tool calls.
func scanSummary(scanner *bufio.Scanner, s *Summary) {
	seen := map[string]bool{}
	for line := 0; line < maxSummaryLines && scanner.Scan(); line++ {
		var r Record
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			continue
		}
		if r.Type == recordBranch && r.Branch != "" {
			s.Branches = append(s.Branches, r.Branch)
			continue
		}
		if r.Message == nil {
			continue
		}
		if s.Prompt == "" && r.Message.Role == agent.RoleUser {
			s.Prompt = openingAsk(r.Message.Content)
		}
		for _, call := range r.Message.ToolCalls {
			path := writtenPath(call)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			s.Touched = append(s.Touched, relativeTo(s.Root, path))
		}
	}
}

// relativeTo shortens a path against the session's root, so a listing shows
// what changed rather than where the worktree happened to be.
func relativeTo(root, path string) string {
	if root == "" || !filepath.IsAbs(path) {
		return path
	}
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// skillPreamble is how a skill invocation opens, and requestMarker is where
// the operator's own words begin. Between them sits the skill's body, so a
// listing that showed the first line would name the skill for every session
// that used one and the task for none.
const (
	skillPreamble = "You are working under the "
	requestMarker = "\nThe request: "
)

// openingAsk returns what the operator actually asked.
//
// A skill folds its instructions into the first message with the request at
// the end. A skill invoked bare has no request at all, in which case its name
// is the most the session can say about itself.
func openingAsk(content string) string {
	if !strings.HasPrefix(content, skillPreamble) {
		return content
	}
	if i := strings.LastIndex(content, requestMarker); i >= 0 {
		if tail := strings.TrimSpace(content[i+len(requestMarker):]); tail != "" {
			return tail
		}
	}
	if name, _, ok := strings.Cut(strings.TrimPrefix(content, skillPreamble), " skill"); ok {
		return "skill " + strings.Trim(name, `"`)
	}
	return content
}

// writtenPath returns the file a tool call changed, or empty for a call that
// only looked. A listing of what a session read would name every file in the
// repository and distinguish nothing.
func writtenPath(call agent.ToolCall) string {
	switch call.Name {
	case "edit", "write":
	default:
		return ""
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(call.Arguments, &args) != nil {
		return ""
	}
	return args.Path
}
