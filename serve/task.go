package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/tools"
)

// A task is one agent run over a repository this process clones itself.
//
// It is the other half of /turn: that one answers about a directory the
// caller already has, this one is given a repository address and works on a
// copy of its own. Nothing is kept — the clone is removed when the run ends,
// because the diff is the answer and a customer's repository is not worth
// holding on to afterwards.
type Task struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Prompt    string     `json:"prompt"`
	BaseRef   string     `json:"base_ref,omitempty"`
	Status    string     `json:"status"`
	Model     string     `json:"model,omitempty"`
	Diff      string     `json:"diff,omitempty"`
	Summary   string     `json:"summary,omitempty"`
	Error     string     `json:"error,omitempty"`
	Usage     TaskUsage  `json:"usage"`
	CreatedAt time.Time  `json:"created_at"`
	SettledAt *time.Time `json:"settled_at,omitempty"`

	// repoURL carries credentials and never leaves this process: it is not a
	// JSON field, so no response and no log can echo it back.
	repoURL string
	steps   []TaskStep
}

// TaskUsage is what a run cost.
type TaskUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	Steps             int   `json:"steps"`
}

// TaskStep is one tool call, as the caller renders it while the run happens.
type TaskStep struct {
	Seq        int    `json:"seq"`
	Tool       string `json:"tool"`
	Arguments  string `json:"arguments,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// Task statuses. The caller polls until one of the settled three.
const (
	TaskPending   = "pending"
	TaskRunning   = "running"
	TaskSucceeded = "succeeded"
	TaskFailed    = "failed"
)

// tasks holds what this process is running and what it has run.
//
// In memory on purpose: a task nobody is waiting for is worth nothing, and a
// restart that loses one is a restart the caller sees as a failure it can
// retry. Persisting them would mean owning a database to serve a queue whose
// longest entry lives thirty minutes.
type taskStore struct {
	mu    sync.Mutex
	byID  map[string]*Task
	order []string
	// max bounds what is kept, so a long-lived process does not grow without
	// end. The oldest settled task is dropped first.
	max int
}

func newTaskStore(max int) *taskStore {
	if max <= 0 {
		max = 200
	}
	return &taskStore{byID: map[string]*Task{}, max: max}
}

func (s *taskStore) add(t *Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[t.ID] = t
	s.order = append(s.order, t.ID)
	for len(s.order) > s.max {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.byID, oldest)
	}
}

// get returns a copy: the caller reads while the run mutates, and handing out
// the live struct would race with every step the agent takes.
func (s *taskStore) get(id string) (Task, []TaskStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byID[id]
	if !ok {
		return Task{}, nil, false
	}
	steps := make([]TaskStep, len(t.steps))
	copy(steps, t.steps)
	return *t, steps, true
}

func (s *taskStore) update(id string, fn func(*Task)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.byID[id]; ok {
		fn(t)
	}
}

// CreateTaskRequest is what the caller queues.
//
// RepoURL carries its own credentials — https://x-access-token:<pat>@… — which
// is how the caller grants access to a private repository without this process
// holding anyone's token.
type CreateTaskRequest struct {
	Kind    string `json:"kind"`
	Prompt  string `json:"prompt"`
	RepoURL string `json:"repo_url"`
	BaseRef string `json:"base_ref,omitempty"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		slog.Warn("task: rejected", "reason", "invalid request body", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		slog.Warn("task: rejected", "reason", "prompt is required")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prompt is required"})
		return
	}
	if strings.TrimSpace(req.RepoURL) == "" {
		slog.Warn("task: rejected", "reason", "repo_url is required")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo_url is required"})
		return
	}
	kind := req.Kind
	if kind != "diagnose" {
		kind = "fix"
	}

	task := &Task{
		ID:        uuid.NewString(),
		Kind:      kind,
		Prompt:    strings.TrimSpace(req.Prompt),
		BaseRef:   strings.TrimSpace(req.BaseRef),
		Status:    TaskPending,
		Model:     s.cfg.Provider.Model,
		CreatedAt: time.Now().UTC(),
		repoURL:   strings.TrimSpace(req.RepoURL),
	}
	s.tasks.add(task)
	slog.Info("task: accepted",
		"task_id", task.ID, "kind", kind, "base_ref", task.BaseRef,
		"prompt_bytes", len(task.Prompt))

	// Answered now, run in the background: a task takes minutes, and holding
	// the request open for it is how a caller times out on work that
	// succeeded. The context is deliberately not the request's — the caller
	// hanging up must not abandon a clone half-made.
	go s.runTask(context.WithoutCancel(r.Context()), task.ID)

	writeJSON(w, http.StatusAccepted, task)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	task, _, ok := s.tasks.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleTaskSteps(w http.ResponseWriter, r *http.Request) {
	_, steps, ok := s.tasks.get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	if steps == nil {
		steps = []TaskStep{}
	}
	writeJSON(w, http.StatusOK, steps)
}

// runTask clones, runs the agent, and settles the task with what it produced.
func (s *Server) runTask(ctx context.Context, id string) {
	task, _, ok := s.tasks.get(id)
	if !ok {
		slog.Error("task: not found, nothing to run", "task_id", id)
		return
	}
	s.tasks.update(id, func(t *Task) { t.Status = TaskRunning })

	started := time.Now()
	log := slog.With("task_id", id, "kind", task.Kind)
	log.Info("task: running", "base_ref", task.BaseRef, "model", task.Model)

	cctx, cancel := context.WithTimeout(ctx, s.taskTimeout())
	defer cancel()

	// Every failure below is recorded on the task and logged here: a task
	// nobody polls would otherwise fail in silence.
	settle := func(status, reason string) {
		now := time.Now().UTC()
		s.tasks.update(id, func(t *Task) {
			t.Status = status
			t.Error = reason
			t.SettledAt = &now
		})
		log.Error("task: failed", "reason", reason, "duration_ms", time.Since(started).Milliseconds())
	}

	dir, err := os.MkdirTemp(s.cfg.WorkDir, "cortex-"+id[:8]+"-")
	if err != nil {
		settle(TaskFailed, fmt.Sprintf("workspace: %v", err))
		return
	}
	defer os.RemoveAll(dir)

	if err := clone(cctx, task.repoURL, task.BaseRef, dir); err != nil {
		settle(TaskFailed, err.Error())
		return
	}
	log.Info("task: cloned", "duration_ms", time.Since(started).Milliseconds())

	client, err := agent.NewClient(s.cfg.Provider)
	if err != nil {
		settle(TaskFailed, fmt.Sprintf("model client: %v", err))
		return
	}

	sink := &taskSink{store: s.tasks, taskID: id}
	runner, err := agent.NewRunner(agent.Config{
		Client:   client,
		Tools:    taskTools(dir, task.Kind),
		System:   taskSystemPrompt(task.Kind),
		MaxSteps: s.cfg.MaxSteps,
		Callback: sink,
	})
	if err != nil {
		settle(TaskFailed, fmt.Sprintf("agent: %v", err))
		return
	}

	res, err := runner.Run(cctx, []agent.Message{{Role: agent.RoleUser, Content: task.Prompt}})
	if err != nil {
		settle(TaskFailed, err.Error())
		return
	}
	log.Info("task: agent settled",
		"steps", res.Steps,
		"input_tokens", res.Usage.Input,
		"cached_input_tokens", res.Usage.CachedInput,
		"output_tokens", res.Usage.Output)

	usage := TaskUsage{
		InputTokens:       int64(res.Usage.Input),
		CachedInputTokens: int64(res.Usage.CachedInput),
		OutputTokens:      int64(res.Usage.Output),
		Steps:             res.Steps,
	}

	diff := ""
	// A diagnose run is not asked to change anything, and reading a diff it
	// did not mean to produce would turn a stray edit into an answer.
	if task.Kind != "diagnose" {
		diff, err = readDiff(cctx, dir)
		if err != nil {
			settle(TaskFailed, err.Error())
			return
		}
	}

	now := time.Now().UTC()
	s.tasks.update(id, func(t *Task) {
		t.Status = TaskSucceeded
		t.Diff = diff
		t.Summary = res.Text
		t.Usage = usage
		t.SettledAt = &now
	})
	log.Info("task: succeeded",
		"diff_bytes", len(diff),
		"duration_ms", time.Since(started).Milliseconds())
}

// clone fetches the repository shallowly: the agent reads code, not history.
func clone(ctx context.Context, repoURL, baseRef, dir string) error {
	args := []string{"clone", "--depth", "1"}
	if baseRef != "" {
		args = append(args, "--branch", baseRef)
	}
	args = append(args, repoURL, dir)
	out, err := runGit(ctx, "", args...)
	if err != nil {
		// The URL carries a token. Report what git said, with the address
		// redacted — an error that quotes it publishes it.
		return fmt.Errorf("clone: %w: %s", err, redact(string(out), repoURL))
	}
	return nil
}

// readDiff reads back everything the agent left, including files it created —
// which `git diff` alone does not report.
func readDiff(ctx context.Context, dir string) (string, error) {
	if out, err := runGit(ctx, dir, "add", "-A"); err != nil {
		return "", fmt.Errorf("stage changes: %w: %s", err, out)
	}
	out, err := runGit(ctx, dir, "diff", "--cached")
	if err != nil {
		return "", fmt.Errorf("read diff: %w: %s", err, out)
	}
	return string(out), nil
}

// taskTools is what the agent may do inside the clone.
//
// A diagnose run is given no way to change anything: it is asked to explain,
// and a tool it does not have is a tool it cannot misuse.
func taskTools(root, kind string) []agent.Tool {
	tracker := tools.NewReadTracker()
	read := []agent.Tool{
		tools.NewRead(tools.ReadConfig{Root: root, Tracker: tracker}),
		tools.NewGrep(tools.GrepConfig{Root: root}),
		tools.NewGlob(tools.GlobConfig{Root: root}),
	}
	if kind == "diagnose" {
		return read
	}
	// Nobody is watching a task run, so there is nobody to approve anything.
	// The confinement is the root, not a prompt.
	approver := tools.AllowAll{}
	return append(read,
		tools.NewEdit(tools.EditConfig{Root: root, Tracker: tracker, Approver: approver}),
		tools.NewWrite(tools.WriteConfig{Root: root, Tracker: tracker, Approver: approver}),
		tools.NewBash(tools.BashConfig{Root: root, Approver: approver}),
	)
}

func taskSystemPrompt(kind string) string {
	const common = `You are working inside a clone of a git repository. Every path you read or write is relative to its root, and nothing outside it exists for you.

Read before you conclude. A claim about code you have not opened is a guess, and a guess reported as a finding is worse than saying you do not know.`

	if kind == "diagnose" {
		return common + `

You are diagnosing. Explain what is wrong and why, citing the files and lines you read. You have no way to change anything, which is deliberate: your answer is the explanation.

If the evidence does not support a conclusion, say what you ruled out and what you would need to go further.`
	}

	return common + `

You are fixing. Make the smallest change that addresses the problem, then verify it: run the tests or the build the repository already has. A fix you did not run is a proposal, and you must say so.

If the request does not hold up against the code — the fault is elsewhere, or already fixed — change nothing and explain why. An empty diff is a legitimate answer and a better one than a plausible edit.

Do not commit, and do not push. What you leave in the working tree is what is read back.`
}

// taskSink records each tool call on the task, so the caller can render a
// timeline while the run happens rather than a spinner.
type taskSink struct {
	agent.NopCallback
	store  *taskStore
	taskID string
	seq    int
}

func (s *taskSink) OnToolEnd(trace agent.ToolCallTrace) {
	s.seq++
	step := TaskStep{
		Seq:        s.seq,
		Tool:       trace.Name,
		Arguments:  trace.Arguments,
		Result:     trace.Result,
		Error:      trace.Err,
		DurationMs: trace.DurationMs,
	}
	s.store.update(s.taskID, func(t *Task) { t.steps = append(t.steps, step) })
}

func (s *Server) taskTimeout() time.Duration {
	if s.cfg.TaskTimeout > 0 {
		return s.cfg.TaskTimeout
	}
	return 30 * time.Minute
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = dir
	// A clone must never stop to ask for credentials: the URL carries them,
	// and a prompt would hang until the timeout.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.CombinedOutput()
}

// redact removes the credentials a clone URL carries from anything stored or
// logged.
func redact(s, repoURL string) string {
	if repoURL == "" {
		return s
	}
	out := strings.ReplaceAll(s, repoURL, "<repo>")
	if at := strings.Index(repoURL, "@"); at > 0 {
		if scheme := strings.Index(repoURL, "://"); scheme > 0 && scheme+3 < at {
			out = strings.ReplaceAll(out, repoURL[scheme+3:at], "<credentials>")
		}
	}
	return out
}
