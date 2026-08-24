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
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/pricing"
	"github.com/lalternative/packages/go/cortex/promptctx"
	"github.com/lalternative/packages/go/cortex/sandbox"
	"github.com/lalternative/packages/go/cortex/session"
	"github.com/lalternative/packages/go/cortex/skills"
	"github.com/lalternative/packages/go/cortex/tools"
	"github.com/lalternative/packages/go/cortex/vision"
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
	// ContextWindow is the model's context length in tokens, which is what
	// lets a conversation be evicted and compacted rather than run into it.
	// Zero disables both.
	ContextWindow int
	// Vision describes images the agent finds in the workspace — a screenshot
	// of a failing page, a diagram, a mockup. Empty Model leaves the tool out
	// rather than offering one that answers every call with an error.
	Vision vision.Config
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
	// describer is set when a vision model is configured, and is what turns a
	// pasted screenshot into something the conversation can carry.
	describer *vision.Describer
	// What each conversation has said and seen, tool results included. The
	// window holds none of it: a turn only makes sense against what the
	// agent already read, and sending that back and forth would drop the
	// eviction and compaction the runner does on it here.
	mu    sync.Mutex
	talks map[string]*talk
}

// talk is one conversation as the agent sees it.
type talk struct {
	history []agent.Message
	usage   agent.Usage
	steps   int
	tools   int
	// store writes each turn to disk as it is produced, so a conversation
	// survives the window that was having it. Nil when sessions could not be
	// opened, which costs the history and nothing else.
	store *session.Store
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
	srv := &Server{
		cfg:    cfg,
		skills: available,
		tasks:  newTaskStore(0),
		talks:  map[string]*talk{},
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
	}
	if strings.TrimSpace(cfg.Vision.Model) != "" {
		describer, err := vision.New(cfg.Vision)
		if err != nil {
			return nil, fmt.Errorf("vision: %w", err)
		}
		srv.describer = describer
		srv.tools = append(srv.tools, tools.NewDescribeImage(tools.ImageConfig{
			Root: cfg.Root, Describer: describer,
		}))
	}
	return srv, nil
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
	mux.HandleFunc("GET /context", s.handleContext)
	mux.HandleFunc("GET /diff", s.handleDiff)
	mux.HandleFunc("GET /sessions", s.handleSessions)
	mux.HandleFunc("POST /sessions/{id}/resume", s.handleResumeSession)
	mux.HandleFunc("POST /describe", s.handleDescribe)

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
	// Conversation is the thread this turn belongs to. Empty starts one, and
	// its id comes back on the first event so the window can carry it.
	Conversation string `json:"conversation,omitempty"`
	// Message is what was just asked. Messages is the older form, kept for a
	// caller that holds the history itself.
	Message  string    `json:"message,omitempty"`
	Messages []Message `json:"messages,omitempty"`
	// Skill names one to work under. Its instructions are prepended to the
	// turn, exactly as the CLI did, so the window sends a name rather than
	// carrying a copy of the body it would have to keep in step.
	Skill    string `json:"skill,omitempty"`
	System   string `json:"system,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

// Message is one turn of the conversation, in the shape the caller stores it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Event is what the window renders as the turn unfolds.
type Event struct {
	Kind string `json:"kind"`
	// Step is which model call this is, so the window can say "step 3" while
	// it waits rather than showing an unqualified spinner.
	Step int `json:"step,omitempty"`
	// Reasoning is what a thinking model worked through before answering. It
	// arrives with the answer and is the window's to show or fold away; a turn
	// that ends with nothing else said still has this.
	Reasoning string `json:"reasoning,omitempty"`
	// Tokens, Threshold and Before carry what an eviction or a compaction
	// moved, for a window that shows how the history is being kept in size.
	Tokens    int    `json:"tokens,omitempty"`
	Threshold int    `json:"threshold,omitempty"`
	Before    int    `json:"before,omitempty"`
	Text      string `json:"text,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Args      string `json:"args,omitempty"`
	Result    string `json:"result,omitempty"`
	Err       string `json:"error,omitempty"`
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
	// ToolCalls is how many the turn made, and Session* is what the whole
	// conversation has cost so far — what the CLI printed under every answer.
	ToolCalls                int `json:"tool_calls"`
	SessionInputTokens       int `json:"session_input_tokens"`
	SessionOutputTokens      int `json:"session_output_tokens"`
	SessionCachedInputTokens int `json:"session_cached_input_tokens"`
	// Cost is in euros, and is absent for a model this cannot price rather
	// than zero — a run that says a real cost is nothing would be worse than
	// one that says nothing.
	Cost        *float64 `json:"cost,omitempty"`
	SessionCost *float64 `json:"session_cost,omitempty"`
	// Seconds is how long the turn took, as the CLI printed under each answer.
	Seconds float64 `json:"seconds"`
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
	asked := strings.TrimSpace(req.Message)
	if asked == "" && len(req.Messages) > 0 {
		asked = req.Messages[len(req.Messages)-1].Content
	}
	if asked == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a message is required"})
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
		asked = skill.Prompt(asked)
	}

	id, conv := s.conversation(req)

	// What is being worked on opens the conversation as its own turn, the way
	// the CLI seeds a session. Pasted in front of the first question instead,
	// it reads as part of it — asked whether it spoke French, the agent went
	// and oriented itself in the repository first.
	if len(conv.history) == 0 {
		// A complete exchange, not a bare user turn: two user messages in a
		// row is a shape some providers reject outright ("no user query found
		// in messages"), and the acknowledgement is what makes the state read
		// as something already seen rather than as the question.
		conv.history = append(conv.history,
			agent.Message{Role: agent.RoleUser, Content: promptctx.Workspace(s.cfg.Root)},
			agent.Message{Role: agent.RoleAssistant, Content: "Noted. What would you like to do?"},
		)
	}
	asking := agent.Message{Role: agent.RoleUser, Content: asked}
	conv.history = append(conv.history, asking)
	if conv.store != nil {
		_ = conv.store.Append(asking)
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

	emit(Event{Kind: "conversation", Text: id})
	started := time.Now()

	sink := &eventSink{emit: emit}
	runner, err := agent.NewRunner(agent.Config{
		Client:   client,
		Tools:    s.tools,
		System:   s.system(req.System),
		MaxSteps: steps,
		// Without it the history only grows, and a long conversation ends
		// against the model's limit rather than being compacted into itself.
		ContextWindow: s.cfg.ContextWindow,
		Stream:        true,
		Callback:      sink,
	})
	if err != nil {
		emit(Event{Kind: "error", Err: err.Error()})
		return
	}

	res, err := runner.Run(r.Context(), conv.history)
	if err != nil {
		emit(Event{Kind: "error", Err: err.Error()})
		return
	}
	s.mu.Lock()
	answered := agent.Message{Role: agent.RoleAssistant, Content: res.Text}
	conv.history = append(conv.history, answered)
	if conv.store != nil {
		_ = conv.store.Append(answered)
		_ = conv.store.AppendUsage(res.Usage, s.cfg.Provider.Model, time.Now())
	}
	conv.usage.Input += res.Usage.Input
	conv.usage.CachedInput += res.Usage.CachedInput
	conv.usage.Output += res.Usage.Output
	conv.steps += res.Steps
	conv.tools += sink.toolCalls
	session := conv.usage
	s.mu.Unlock()

	emit(Event{
		Kind:      "message",
		Text:      res.Text,
		Reasoning: res.Reasoning,
		Usage: &Usage{
			InputTokens:              res.Usage.Input,
			CachedInputTokens:        res.Usage.CachedInput,
			OutputTokens:             res.Usage.Output,
			Steps:                    res.Steps,
			ToolCalls:                sink.toolCalls,
			SessionInputTokens:       session.Input,
			SessionCachedInputTokens: session.CachedInput,
			SessionOutputTokens:      session.Output,
			Cost:                     priced(s.cfg.Provider.Model, res.Usage),
			SessionCost:              priced(s.cfg.Provider.Model, session),
			Seconds:                  time.Since(started).Seconds(),
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
	// How to work, and then what is being worked on. Without the first the
	// agent has tools and no idea what is expected of it, and answers a
	// one-line question with twenty commands; the CLI never ran without it.
	if base, err := promptctx.System(promptctx.Options{Root: s.cfg.Root}); err == nil {
		b.WriteString(base)
		b.WriteString("\n\n")
	} else {
		slog.Warn("serve: system prompt unavailable", "error", err)
	}
	return strings.TrimSpace(b.String())
}

// handleWorkspace reports what this machine is offering, so a window can say
// which repository it is pointed at before anything is asked of it.
// priced converts what a turn used into euros, or nothing when the model is
// one the table does not carry.
func priced(model string, u agent.Usage) *float64 {
	cost, known := pricing.Published.Cost(model, u.Input, u.CachedInput, u.Output)
	if !known {
		return nil
	}
	return &cost
}

// conversation finds the thread this turn belongs to, or opens one.
//
// A caller that sends its own history — the older form — gets a thread seeded
// with it, so both callers reach the same runner.
func (s *Server) conversation(req TurnRequest) (string, *talk) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id := strings.TrimSpace(req.Conversation); id != "" {
		if found, ok := s.talks[id]; ok {
			return id, found
		}
	}

	id := uuid.NewString()
	opened := &talk{}
	if req.Message == "" && len(req.Messages) > 1 {
		opened.history = toAgentMessages(req.Messages[:len(req.Messages)-1])
	}
	// Written as it goes, so a conversation outlives the window having it.
	// A store that cannot be opened costs the transcript and nothing else.
	if store, _, err := session.Create(time.Now(), s.cfg.Root, s.cfg.Provider.Model, firstAsk(req)); err == nil {
		opened.store = store
	} else {
		slog.Warn("serve: session not recorded", "error", err)
	}
	s.talks[id] = opened
	return id, opened
}

// SessionSummary is an earlier conversation, as a list needs it.
type SessionSummary struct {
	ID      string   `json:"id"`
	Root    string   `json:"root"`
	Model   string   `json:"model"`
	Started string   `json:"started"`
	Prompt  string   `json:"prompt"`
	Touched []string `json:"touched,omitempty"`
}

// handleSessions lists what was said here before, most recent first.
//
// Only this workspace's, and only the ones that got as far as a question: a
// session opened and abandoned has nothing to recognise it by.
func (s *Server) handleSessions(w http.ResponseWriter, _ *http.Request) {
	earlier, err := session.List(40)
	if err != nil {
		writeJSON(w, http.StatusOK, []SessionSummary{})
		return
	}
	here := make([]SessionSummary, 0, len(earlier))
	for _, e := range earlier {
		if e.Root != s.cfg.Root || strings.TrimSpace(e.Prompt) == "" {
			continue
		}
		here = append(here, SessionSummary{
			ID:      e.ID,
			Root:    e.Root,
			Model:   e.Model,
			Started: e.Started.Format(time.RFC3339),
			Prompt:  e.Prompt,
			Touched: e.Touched,
		})
	}
	writeJSON(w, http.StatusOK, here)
}

// handleResumeSession opens an earlier conversation as a live one, so the
// next turn continues it rather than starting over.
func (s *Server) handleResumeSession(w http.ResponseWriter, r *http.Request) {
	loaded, err := session.Load(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	id := uuid.NewString()
	s.mu.Lock()
	s.talks[id] = &talk{history: loaded.Messages}
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"conversation": id,
		"messages":     len(loaded.Messages),
		"prompt":       loaded.Prompt,
	})
}

// DescribeRequest is a pasted image and what to ask about it.
//
// A screenshot dropped into a window has no path — it is bytes on a clipboard
// — so it arrives inline rather than as a filename the read tool could take.
type DescribeRequest struct {
	// Image is base64, with or without a data: prefix.
	Image    string `json:"image"`
	MimeType string `json:"mime_type,omitempty"`
	Question string `json:"question,omitempty"`
}

func (s *Server) handleDescribe(w http.ResponseWriter, r *http.Request) {
	if s.describer == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "no vision model is configured",
		})
		return
	}
	var req DescribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	encoded := req.Image
	if i := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && i > 0 {
		if req.MimeType == "" {
			req.MimeType = strings.TrimSuffix(strings.TrimPrefix(encoded[:i], "data:"), ";base64")
		}
		encoded = encoded[i+1:]
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the image is not base64"})
		return
	}

	described, err := s.describer.DescribeBytes(r.Context(), data, req.MimeType, req.Question)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"description": described})
}

// Diff is what has changed in the workspace since the last commit.
//
// The agent edits files, and the only honest account of what a session did is
// the tree it left behind — not what it said it did.
type Diff struct {
	Stat  string `json:"stat"`
	Patch string `json:"patch"`
	Clean bool   `json:"clean"`
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	root := strings.TrimSpace(s.cfg.Root)
	if root == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this instance has no workspace"})
		return
	}
	stat, err := gitOutput(r.Context(), root, "diff", "--stat")
	if err != nil {
		writeJSON(w, http.StatusOK, Diff{Clean: true})
		return
	}
	patch := ""
	// The full patch only when there is something to patch: on a large tree
	// it is megabytes, and the window asked for a summary first.
	if strings.TrimSpace(stat) != "" {
		patch, _ = gitOutput(r.Context(), root, "diff")
	}
	writeJSON(w, http.StatusOK, Diff{
		Stat:  stat,
		Patch: patch,
		Clean: strings.TrimSpace(stat) == "",
	})
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	return string(out), err
}

// ContextView is what the model is actually being sent.
//
// Guessing at this is how a prompt bug survives: a window that shows it can
// see that the system prompt ends where it should, that the history holds the
// tool results, and how close the conversation is to the limit it will be
// compacted at.
type ContextView struct {
	Model         string       `json:"model"`
	BaseURL       string       `json:"base_url"`
	ContextWindow int          `json:"context_window"`
	System        string       `json:"system"`
	SystemTokens  int          `json:"system_tokens"`
	Tools         []string     `json:"tools"`
	Messages      []ViewedTurn `json:"messages"`
	HistoryTokens int          `json:"history_tokens"`
	Usage         Usage        `json:"usage"`
}

// ViewedTurn is one message as it stands in the history, tool calls included.
type ViewedTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Tokens    int    `json:"tokens"`
	ToolName  string `json:"tool_name,omitempty"`
	ToolCalls int    `json:"tool_calls,omitempty"`
}

func (s *Server) handleContext(w http.ResponseWriter, r *http.Request) {
	view := ContextView{
		Model:         s.cfg.Provider.Model,
		BaseURL:       s.cfg.Provider.BaseURL,
		ContextWindow: s.cfg.ContextWindow,
		System:        s.system(""),
	}
	view.SystemTokens = agent.EstimateTokens(view.System)
	for _, tool := range s.tools {
		view.Tools = append(view.Tools, tool.Name())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	conv, ok := s.talks[strings.TrimSpace(r.URL.Query().Get("conversation"))]
	if !ok {
		writeJSON(w, http.StatusOK, view)
		return
	}
	for _, m := range conv.history {
		turn := ViewedTurn{
			Role:      string(m.Role),
			Content:   m.Content,
			Tokens:    agent.EstimateTokens(m.Content),
			ToolCalls: len(m.ToolCalls),
		}
		if len(m.ToolCalls) == 1 {
			turn.ToolName = m.ToolCalls[0].Name
		}
		view.HistoryTokens += turn.Tokens
		view.Messages = append(view.Messages, turn)
	}
	view.Usage = Usage{
		InputTokens:              conv.usage.Input,
		CachedInputTokens:        conv.usage.CachedInput,
		OutputTokens:             conv.usage.Output,
		Steps:                    conv.steps,
		ToolCalls:                conv.tools,
		SessionInputTokens:       conv.usage.Input,
		SessionCachedInputTokens: conv.usage.CachedInput,
		SessionOutputTokens:      conv.usage.Output,
		SessionCost:              priced(s.cfg.Provider.Model, conv.usage),
	}
	writeJSON(w, http.StatusOK, view)
}

// firstAsk is what a session is recognised by later: the question it opened
// with, before any skill wrapped it.
func firstAsk(req TurnRequest) string {
	if m := strings.TrimSpace(req.Message); m != "" {
		return m
	}
	if len(req.Messages) > 0 {
		return strings.TrimSpace(req.Messages[len(req.Messages)-1].Content)
	}
	return ""
}

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
	step      int
	toolCalls int
	agent.NopCallback
	emit func(Event)
}

func (s *eventSink) OnTextDelta(text string) {
	s.emit(Event{Kind: "delta", Text: text})
}

func (s *eventSink) OnStepStart(step int) {
	// The window says "step 3 · thinking" while it waits, as the CLI did: a
	// spinner that never says what it is doing reads like a hang.
	s.step = step
	s.emit(Event{Kind: "step", Step: step})
}

func (s *eventSink) OnToolStart(name string, args json.RawMessage) {
	s.toolCalls++
	s.emit(Event{Kind: "tool_start", Step: s.step, Tool: name, Args: string(args)})
}

func (s *eventSink) OnToolEnd(trace agent.ToolCallTrace) {
	result := trace.Result
	if trace.Err != "" {
		result = trace.Err
	}
	s.emit(Event{Kind: "tool_end", Tool: trace.Name, Args: trace.Arguments, Result: result})
}

// What the loop does to the history to keep it inside the window: dropping
// the oldest tool results, then summarising what is left. Both change what
// the model is answering from, so a window that shows the conversation
// should be able to say when they happened.
func (s *eventSink) OnEvict(freedTokens int) {
	s.emit(Event{Kind: "evict", Step: s.step, Tokens: freedTokens})
}

func (s *eventSink) OnCompactStart(usedTokens, thresholdTokens int) {
	s.emit(Event{Kind: "compact_start", Step: s.step, Tokens: usedTokens, Threshold: thresholdTokens})
}

func (s *eventSink) OnCompactEnd(beforeTokens, afterTokens int) {
	s.emit(Event{Kind: "compact_end", Step: s.step, Tokens: afterTokens, Before: beforeTokens})
}

func (s *eventSink) OnError(err error) {
	s.emit(Event{Kind: "error", Err: err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
