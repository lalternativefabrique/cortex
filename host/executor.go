package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
	"github.com/google/uuid"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/mcp"
	"github.com/lalternative/packages/go/cortex/recall"
)

// executor answers one A2A task as one turn of the agent. The answer is one
// artifact, "answer", streamed in text chunks; every tool call is its own
// "tool-call" artifact carrying what it was asked and what it returned.
type executor struct {
	host *Host
}

var _ a2asrv.AgentExecutor = (*executor)(nil)

func (e *executor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	subject, headers, turnContext := metadata(reqCtx.Message)
	ctx = withTurnToken(ctx, reqCtx.Message)
	log := taskLogger(e.host, reqCtx).With("subject", subject)
	started := time.Now()
	log.InfoContext(ctx, "cortex: task started", "resumed", reqCtx.StoredTask != nil)
	end := func(state a2a.TaskState, why string) error {
		logTaskEnd(ctx, log, state, why, time.Since(started))
		return finish(ctx, queue, reqCtx, state, why)
	}

	if reqCtx.StoredTask == nil {
		if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateSubmitted, nil)); err != nil {
			return err
		}
	}
	asked := messageText(reqCtx.Message)
	if asked == "" {
		return end(a2a.TaskStateFailed, "a text message is required")
	}
	if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateWorking, nil)); err != nil {
		return err
	}
	log.InfoContext(ctx, "cortex: task working")

	h := e.host
	tools := append(append([]agent.Tool(nil), h.cfg.Tools...), h.servers.forTurn()...)
	var memory *recall.Recorder
	if h.cfg.Recall != nil && subject != "" {
		scope := recall.Scope{Subject: subject, Agent: h.cfg.Agent.Name}
		tools = append(tools, recall.NewTool(h.cfg.Recall, scope))
		memory = recall.NewRecorder(ctx, h.cfg.Recall, scope, reqCtx.ContextID)
	}
	provider := h.cfg.Provider
	base := instructions(h.cfg.Agent.Instructions, h.appInstructionsNow())
	system := instructions(base, turnContext)
	schema, hasSchema := outputSchema(reqCtx.Message.Metadata)
	if name, _ := reqCtx.Message.Metadata[SkillKey].(string); strings.TrimSpace(name) != "" {
		_, skills := h.declaredSkills()
		declared, ok := skills[strings.TrimSpace(name)]
		if !ok {
			return end(a2a.TaskStateFailed, fmt.Sprintf("this agent declares no skill %q", name))
		}
		if err := declared.checkInput(asked); err != nil {
			return end(a2a.TaskStateFailed, err.Error())
		}
		log = log.With("skill", declared.ID)
		system = instructions(instructions(base, declared.Instructions), turnContext)
		if schema2, ok := declared.outputSchema(); ok {
			schema, hasSchema = schema2, true
		}
		if declared.Model != "" {
			provider.Model = declared.Model
		}
	}
	if model, _ := reqCtx.Message.Metadata[ModelKey].(string); strings.TrimSpace(model) != "" {
		provider.Model = strings.TrimSpace(model)
	}
	log = log.With("model", provider.Model)
	client, err := agent.NewClient(provider)
	if err != nil {
		return end(a2a.TaskStateFailed, err.Error())
	}
	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var respond *respondTool
	if hasSchema {
		respond, err = newRespondTool(schema, stopRun)
		if err != nil {
			return end(a2a.TaskStateFailed, err.Error())
		}
		tools = append(tools, respond)
		system = withRespondInstruction(system)
	}
	stream := &stream{ctx: ctx, queue: queue, task: reqCtx, memory: memory, answer: a2a.NewArtifactID(), structured: respond != nil}
	runner, err := agent.NewRunner(agent.Config{
		Client:        client,
		Tools:         tools,
		System:        system,
		MaxSteps:      h.cfg.MaxSteps,
		ContextWindow: h.cfg.ContextWindow,
		Stream:        true,
		Callback:      stream,
		Logger:        log,
	})
	if err != nil {
		return end(a2a.TaskStateFailed, err.Error())
	}

	asking := agent.Message{Role: agent.RoleUser, Content: asked}
	conversation := conversationKey(subject, reqCtx.ContextID)
	history := append(h.conversations.history(conversation), asking)
	memory.Said(reqCtx.Message.ID, "user", asked)

	runWith := mcp.WithHeaders(runCtx, headers)
	res, err := runner.Run(runWith, history)
	if respond != nil {
		return e.endStructured(ctx, end, stream, respond, runner, runWith, conversation, asking, history, res, err)
	}
	if err != nil {
		return end(a2a.TaskStateFailed, err.Error())
	}
	h.conversations.append(conversation, asking, agent.Message{Role: agent.RoleAssistant, Content: res.Text})
	memory.Said(uuid.NewString(), "assistant", res.Text)

	if err := stream.close(res.Text); err != nil {
		return err
	}
	return end(a2a.TaskStateCompleted, "")
}

// endStructured finishes a turn that asked for an output schema: the answer
// is what respond recorded. A turn that ended in text instead is reminded
// to call respond, a bounded number of times.
func (e *executor) endStructured(ctx context.Context, end func(a2a.TaskState, string) error, stream *stream, respond *respondTool,
	runner *agent.Runner, runCtx context.Context, conversation string, asking agent.Message, history []agent.Message, res agent.Result, err error,
) error {
	for reminders := 0; ; reminders++ {
		if answer, ok := respond.recorded(); ok {
			e.host.conversations.append(conversation, asking, agent.Message{Role: agent.RoleAssistant, Content: string(answer)})
			stream.memory.Said(uuid.NewString(), "assistant", string(answer))
			if err := stream.data(answer); err != nil {
				return err
			}
			return end(a2a.TaskStateCompleted, "")
		}
		if err != nil {
			return end(a2a.TaskStateFailed, err.Error())
		}
		if reminders == maxRespondReminders {
			return end(a2a.TaskStateFailed, "the agent did not answer in the required format")
		}
		history = append(history,
			agent.Message{Role: agent.RoleAssistant, Content: res.Text},
			agent.Message{Role: agent.RoleUser, Content: respondReminder})
		res, err = runner.Run(runCtx, history)
	}
}

func (e *executor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	taskLogger(e.host, reqCtx).InfoContext(ctx, "cortex: task canceled")
	return finish(ctx, queue, reqCtx, a2a.TaskStateCanceled, "")
}

func taskLogger(h *Host, reqCtx *a2asrv.RequestContext) *slog.Logger {
	return slog.Default().With(
		"agent", h.cfg.Agent.Name,
		"context_id", reqCtx.ContextID,
		"task_id", string(reqCtx.TaskID),
	)
}

func logTaskEnd(ctx context.Context, log *slog.Logger, state a2a.TaskState, why string, took time.Duration) {
	ms := took.Milliseconds()
	if state == a2a.TaskStateFailed {
		log.ErrorContext(ctx, "cortex: task failed", "duration_ms", ms, "error", why)
		return
	}
	log.InfoContext(ctx, "cortex: task "+string(state), "duration_ms", ms)
}

// finish closes the task; a2a-go ends a stream on a final event only.
func finish(ctx context.Context, queue eventqueue.Queue, reqCtx *a2asrv.RequestContext, state a2a.TaskState, why string) error {
	var msg *a2a.Message
	if why != "" {
		msg = a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: why})
	}
	ev := a2a.NewStatusUpdateEvent(reqCtx, state, msg)
	ev.Final = true
	return queue.Write(ctx, ev)
}

func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var parts []string
	for _, p := range msg.Parts {
		if t, ok := p.(a2a.TextPart); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func instructions(base, turnContext string) string {
	if turnContext == "" {
		return base
	}
	return strings.TrimSpace(base + "\n\n" + turnContext)
}

func metadata(msg *a2a.Message) (string, map[string]string, string) {
	if msg == nil {
		return "", nil, ""
	}
	subject, _ := msg.Metadata[SubjectKey].(string)
	turnContext, _ := msg.Metadata[TurnContextKey].(string)
	raw, _ := msg.Metadata[MCPHeadersKey].(map[string]any)
	headers := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	return strings.TrimSpace(subject), headers, strings.TrimSpace(turnContext)
}

// stream writes the turn to the task as it happens.
type stream struct {
	agent.NopCallback
	ctx    context.Context
	queue  eventqueue.Queue
	task   *a2asrv.RequestContext
	memory *recall.Recorder
	answer a2a.ArtifactID
	// structured holds text back: the answer is what respond records.
	structured bool

	mu        sync.Mutex
	stepStart time.Time
	started   bool
	// pending is the latest chunk, held back so the last one can be marked
	// lastChunk without an empty part after it.
	pending string
}

func (s *stream) OnStepStart(int) {
	s.mu.Lock()
	s.stepStart = time.Now()
	s.mu.Unlock()
}

// OnModelEnd reports each model step as a "step" artifact: what the model
// reasoned, which tools it asked for, what it cost in tokens and how long it
// took, so a caller can follow the turn's thinking.
func (s *stream) OnModelEnd(step int, _ string, reasoning string, toolCalls []agent.ToolCall, usage agent.Usage) {
	names := make([]string, 0, len(toolCalls))
	for _, c := range toolCalls {
		names = append(names, c.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data := map[string]any{
		"step":                step,
		"reasoning":           reasoning,
		"tool_calls":          names,
		"input_tokens":        usage.Input,
		"cached_input_tokens": usage.CachedInput,
		"output_tokens":       usage.Output,
	}
	if !s.stepStart.IsZero() {
		data["duration_ms"] = time.Since(s.stepStart).Milliseconds()
	}
	ev := a2a.NewArtifactEvent(s.task, a2a.DataPart{Data: data})
	ev.Artifact.Name = "step"
	if err := s.queue.Write(s.ctx, ev); err != nil {
		slog.Warn("host: step not reported", "step", step, "error", err)
	}
}

func (s *stream) OnTextDelta(text string) {
	if text == "" || s.structured {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != "" {
		_ = s.queue.Write(s.ctx, s.chunk(s.pending, false))
	}
	s.pending = text
}

func (s *stream) chunk(text string, last bool) *a2a.TaskArtifactUpdateEvent {
	ev := a2a.NewArtifactUpdateEvent(s.task, s.answer, a2a.TextPart{Text: text})
	ev.Artifact.Name = "answer"
	ev.Append = s.started
	ev.LastChunk = last
	s.started = true
	return ev
}

// close sends the held-back chunk as the last one, or the whole text when
// the model streamed none.
func (s *stream) close(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != "" {
		text, s.pending = s.pending, ""
	} else if s.started {
		return nil
	}
	return s.queue.Write(s.ctx, s.chunk(text, true))
}

// data sends a structured answer as the answer artifact's one data part.
func (s *stream) data(answer json.RawMessage) error {
	var decoded any
	if err := json.Unmarshal(answer, &decoded); err != nil {
		return err
	}
	part := a2a.DataPart{Data: map[string]any{"value": decoded}}
	if object, ok := decoded.(map[string]any); ok {
		part = a2a.DataPart{Data: object}
	}
	ev := a2a.NewArtifactEvent(s.task, part)
	ev.Artifact.Name = "answer"
	ev.LastChunk = true
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queue.Write(s.ctx, ev)
}

func (s *stream) OnToolEnd(trace agent.ToolCallTrace) {
	if s.structured && trace.Name == respondName {
		return
	}
	s.memory.OnToolEnd(trace)
	var args any = trace.Arguments
	var decoded any
	if json.Unmarshal([]byte(trace.Arguments), &decoded) == nil {
		args = decoded
	}
	data := map[string]any{"tool": trace.Name, "arguments": args, "result": trace.Result}
	if trace.Err != "" {
		data["error"] = trace.Err
	}
	ev := a2a.NewArtifactEvent(s.task, a2a.DataPart{Data: data})
	ev.Artifact.Name = "tool-call"
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.queue.Write(s.ctx, ev); err != nil {
		slog.Warn("host: tool call not reported", "tool", trace.Name, "error", err)
	}
}
