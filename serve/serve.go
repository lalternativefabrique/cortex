// Package serve runs the agent on this machine, for a front end that is not
// on it.
//
// The desktop app shows a conversation served from somewhere else — the
// session, the history and what it cost live on a server. What that server
// cannot do is read the repository someone is working in right now: the
// uncommitted change, the branch in progress, the test that only passes with
// their environment. This is what closes that gap. The window talks to a
// server for who you are, and to this for what you have.
package serve

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/promptctx"
	"github.com/lalternative/packages/go/cortex/sandbox"
	"github.com/lalternative/packages/go/cortex/skills"
	"github.com/lalternative/packages/go/cortex/tools"
)

// Config parameterises the local server.
type Config struct {
	// Root is the workspace every tool is confined to. Every path read or
	// written resolves under it, and nothing outside it exists for the agent.
	Root string
	// Provider is the model this machine calls. Calling it from here rather
	// than through the server keeps the repository's contents on this machine:
	// what leaves is the conversation, not the code.
	Provider agent.Provider
	// Addr is where to listen. Empty means 127.0.0.1 on a free port, which is
	// the only address that makes sense — this exposes a shell.
	Addr string
	// Token authenticates the window. Empty generates one, printed on start.
	Token string
	// MaxSteps bounds one turn. Zero leaves the agent's own default.
	MaxSteps int
	// Approver gates writes and commands. Nil approves everything, which is
	// right when the person asking is the person whose machine it is.
	Approver tools.Approver
	// WorkDir is where a task clones. Empty uses the OS temp directory, which
	// is right on a workstation and wrong in a container with a read-only
	// root — there the deployment mounts one and names it here.
	WorkDir string
	// TaskTimeout bounds one task. Zero means thirty minutes.
	TaskTimeout time.Duration
}

// Server answers turns from a front end running elsewhere.
type Server struct {
	cfg    Config
	tools  []agent.Tool
	skills skills.Set
	tasks  *taskStore
}

// New builds the server and the tool set it exposes.
func New(cfg Config) (*Server, error) {
	// Root is what /turn serves. A deployment that only runs tasks has no
	// directory to offer and does not need one — each task brings its own.
	if strings.TrimSpace(cfg.Root) == "" && strings.TrimSpace(cfg.WorkDir) == "" {
		return nil, fmt.Errorf("serve: one of Root or WorkDir is required")
	}
	if cfg.Approver == nil {
		cfg.Approver = tools.AllowAll{}
	}
	tracker := tools.NewReadTracker()
	// A skill that cannot be read is one the window will not offer. That is a
	// worse start than no skills at all, so a broken directory is not fatal.
	available, err := skills.Load(cfg.Root)
	if err != nil {
		slog.Warn("serve: skills unavailable", "error", err)
	}
	return &Server{
		cfg:    cfg,
		skills: available,
		tasks:  newTaskStore(0),
		tools: []agent.Tool{
			tools.NewRead(tools.ReadConfig{Root: cfg.Root, Tracker: tracker}),
			tools.NewGrep(tools.GrepConfig{Root: cfg.Root}),
			tools.NewGlob(tools.GlobConfig{Root: cfg.Root}),
			tools.NewEdit(tools.EditConfig{Root: cfg.Root, Tracker: tracker, Approver: cfg.Approver}),
			tools.NewWrite(tools.WriteConfig{Root: cfg.Root, Tracker: tracker, Approver: cfg.Approver}),
			tools.NewBash(tools.BashConfig{
				Root:     cfg.Root,
				Sandbox:  sandbox.NewDirect(),
				Approver: cfg.Approver,
			}),
		},
	}, nil
}

// Listen binds the address and returns the listener, so the caller can print
// the real port before serving on it.
func (s *Server) Listen() (net.Listener, error) {
	addr := s.cfg.Addr
	if addr == "" {
		// Loopback only, never a wildcard: this endpoint runs shell commands,
		// and a machine on the same network is not the person at the keyboard.
		addr = "127.0.0.1:0"
	}
	return net.Listen("tcp", addr)
}

// Serve answers requests until the listener is closed.
func (s *Server) Serve(ln net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /turn", s.handleTurn)
	// Tasks clone a repository of their own, where /turn answers about the
	// directory this process was started in. The paths match what the caller
	// already speaks, so nothing about it has to know which of the two is
	// answering.
	mux.HandleFunc("POST /api/v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /api/v1/tasks/{id}/steps", s.handleTaskSteps)
	mux.HandleFunc("GET /workspace", s.handleWorkspace)
	mux.HandleFunc("GET /skills", s.handleSkills)

	// A probe holds no token, and a readiness check that answers 401 is a
	// check that never passes: the kubelet counts only 2xx-3xx as healthy.
	// So this is served ahead of the token, and says nothing a caller without
	// one should not learn -- the workspace path stays behind /workspace.
	root := http.NewServeMux()
	root.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.Handle("/", s.authenticated(mux))
	return (&http.Server{
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
	}).Serve(ln)
}

// authenticated refuses anything without the token.
//
// Loopback is not a permission: every process on this machine can reach it,
// and a page in a browser can too. The token is what separates the window
// that was launched with it from everything else.
func (s *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The window is served from another origin, so it asks first. A
		// preflight carries no Authorization header — no browser sends one —
		// and answering it with a 401 stops the real request from ever being
		// made, which is why this comes before the token is checked.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if s.cfg.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != s.cfg.Token {
				// The caller is a deployment, not a person: a 401 here means
				// its token was never configured or no longer matches, and
				// saying so beats a queue that silently never drains. The
				// token itself is never logged, only whether one was sent.
				slog.Warn("http: unauthorized",
					"method", r.Method, "path", r.URL.Path,
					"token_sent", got != "", "remote", r.RemoteAddr)
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// TurnRequest is one exchange: the history so far, and what to answer.
//
// The history comes from the server that holds it. This machine keeps none —
// it runs a turn and forgets it, which is what lets the same conversation
// continue on a phone that has no repository at all.
type TurnRequest struct {
	Messages []Message `json:"messages"`
	// Skill names one to work under. Its instructions are prepended to the
	// turn, exactly as the CLI did, so the window sends a name rather than
	// carrying a copy of the body it would have to keep in step.
	Skill string `json:"skill,omitempty"`
	System   string    `json:"system,omitempty"`
	MaxSteps int       `json:"max_steps,omitempty"`
}

// Message is one turn of the conversation, in the shape the caller stores it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Event is what the window renders as the turn unfolds.
type Event struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Err    string `json:"error,omitempty"`
	// Usage arrives once, at the end: the caller records what the turn cost
	// against the account it belongs to.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage is what one turn consumed.
type Usage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	Steps             int `json:"steps"`
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	// A deployment that only runs tasks has no directory to answer about, and
	// every tool would resolve against whatever the process happens to sit in.
	if strings.TrimSpace(s.cfg.Root) == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "this instance serves tasks only; it has no workspace to answer about",
		})
		return
	}

	var req TurnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "messages are required"})
		return
	}

	// The skill wraps what was just asked, not the history: replaying it over
	// every past turn would repeat its instructions once per exchange.
	if name := strings.TrimSpace(req.Skill); name != "" {
		skill, found := s.skills.Lookup(name)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": fmt.Sprintf("no skill named %q", name),
			})
			return
		}
		last := len(req.Messages) - 1
		req.Messages[last].Content = skill.Prompt(req.Messages[last].Content)
	}

	client, err := agent.NewClient(s.cfg.Provider)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	// Tools run on their own goroutines and report through the same writer as
	// the loop, and the heartbeat below writes from a third.
	var writing sync.Mutex
	write := func(frame string) {
		writing.Lock()
		defer writing.Unlock()
		fmt.Fprint(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit := func(e Event) {
		payload, err := json.Marshal(e)
		if err != nil {
			return
		}
		write(fmt.Sprintf("data: %s\n\n", payload))
	}

	// A step can think for a minute with nothing to say, and a connection that
	// carries no bytes for that long is one an intermediary — a proxy, or the
	// engine rendering the window — is free to consider dead. A comment frame
	// is ignored by every SSE reader and keeps the connection accounted for.
	done := make(chan struct{})
	defer close(done)
	go func() {
		beat := time.NewTicker(10 * time.Second)
		defer beat.Stop()
		for {
			select {
			case <-done:
				return
			case <-r.Context().Done():
				return
			case <-beat.C:
				write(": keep-alive\n\n")
			}
		}
	}()

	steps := req.MaxSteps
	if steps == 0 {
		steps = s.cfg.MaxSteps
	}

	sink := &eventSink{emit: emit}
	runner, err := agent.NewRunner(agent.Config{
		Client:   client,
		Tools:    s.tools,
		System:   s.system(req.System),
		MaxSteps: steps,
		Stream:   true,
		Callback: sink,
	})
	if err != nil {
		emit(Event{Kind: "error", Err: err.Error()})
		return
	}

	res, err := runner.Run(r.Context(), toAgentMessages(req.Messages))
	if err != nil {
		emit(Event{Kind: "error", Err: err.Error()})
		return
	}
	emit(Event{
		Kind: "message",
		Text: res.Text,
		Usage: &Usage{
			InputTokens:       res.Usage.Input,
			CachedInputTokens: res.Usage.CachedInput,
			OutputTokens:      res.Usage.Output,
			Steps:             res.Steps,
		},
	})
	emit(Event{Kind: "done"})
}

// system prefixes the caller's instructions with what this machine is.
//
// The caller writes the conversation's own prompt and knows nothing about
// where it runs; the workspace description is what only this side has.
func (s *Server) system(caller string) string {
	var b strings.Builder
	if strings.TrimSpace(caller) != "" {
		b.WriteString(strings.TrimSpace(caller))
		b.WriteString("\n\n")
	}
	b.WriteString(promptctx.Workspace(s.cfg.Root))
	return b.String()
}

// handleWorkspace reports what this machine is offering, so a window can say
// which repository it is pointed at before anything is asked of it.
// SkillSummary is a skill as the window needs it: enough to offer, never the
// body, which is the agent's business and not something to render.
type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (s *Server) handleSkills(w http.ResponseWriter, _ *http.Request) {
	summaries := make([]SkillSummary, 0, len(s.skills))
	for _, name := range s.skills.Names() {
		skill, ok := s.skills.Lookup(name)
		if !ok {
			continue
		}
		summaries = append(summaries, SkillSummary{Name: skill.Name, Description: skill.Description})
	}
	writeJSON(w, http.StatusOK, summaries)
}

func (s *Server) handleWorkspace(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"root":    s.cfg.Root,
		"context": promptctx.Workspace(s.cfg.Root),
	})
}

func toAgentMessages(in []Message) []agent.Message {
	out := make([]agent.Message, 0, len(in))
	for _, m := range in {
		role := agent.RoleUser
		switch m.Role {
		case "assistant":
			role = agent.RoleAssistant
		case "system":
			role = agent.RoleSystem
		}
		out = append(out, agent.Message{Role: role, Content: m.Content})
	}
	return out
}

// eventSink turns the agent's callbacks into stream events.
type eventSink struct {
	agent.NopCallback
	emit func(Event)
}

func (s *eventSink) OnTextDelta(text string) {
	s.emit(Event{Kind: "delta", Text: text})
}

func (s *eventSink) OnToolStart(name string, args json.RawMessage) {
	s.emit(Event{Kind: "tool_start", Tool: name, Args: string(args)})
}

func (s *eventSink) OnToolEnd(trace agent.ToolCallTrace) {
	result := trace.Result
	if trace.Err != "" {
		result = trace.Err
	}
	s.emit(Event{Kind: "tool_end", Tool: trace.Name, Args: trace.Arguments, Result: result})
}

func (s *eventSink) OnError(err error) {
	s.emit(Event{Kind: "error", Err: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
