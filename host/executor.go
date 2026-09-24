package host

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

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
	if reqCtx.StoredTask == nil {
		if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateSubmitted, nil)); err != nil {
			return err
		}
	}
	asked := messageText(reqCtx.Message)
	if asked == "" {
		return finish(ctx, queue, reqCtx, a2a.TaskStateFailed, "a text message is required")
	}
	subject, headers := metadata(reqCtx.Message)
	if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateWorking, nil)); err != nil {
		return err
	}

	h := e.host
	tools := append([]agent.Tool(nil), h.tools...)
	var memory *recall.Recorder
	if h.cfg.Recall != nil && subject != "" {
		scope := recall.Scope{Subject: subject, Agent: h.cfg.Agent.Name}
		tools = append(tools, recall.NewTool(h.cfg.Recall, scope))
		memory = recall.NewRecorder(ctx, h.cfg.Recall, scope, reqCtx.ContextID)
	}
	client, err := agent.NewClient(h.cfg.Provider)
	if err != nil {
		return finish(ctx, queue, reqCtx, a2a.TaskStateFailed, err.Error())
	}
	stream := &stream{ctx: ctx, queue: queue, task: reqCtx, memory: memory, answer: a2a.NewArtifactID()}
	runner, err := agent.NewRunner(agent.Config{
		Client:        client,
		Tools:         tools,
		System:        h.cfg.Agent.Instructions,
		MaxSteps:      h.cfg.MaxSteps,
		ContextWindow: h.cfg.ContextWindow,
		Stream:        true,
		Callback:      stream,
	})
	if err != nil {
		return finish(ctx, queue, reqCtx, a2a.TaskStateFailed, err.Error())
	}

	asking := agent.Message{Role: agent.RoleUser, Content: asked}
	history := append(h.conversations.history(reqCtx.ContextID), asking)
	memory.Said(reqCtx.Message.ID, "user", asked)

	res, err := runner.Run(mcp.WithHeaders(ctx, headers), history)
	if err != nil {
		return finish(ctx, queue, reqCtx, a2a.TaskStateFailed, err.Error())
	}
	h.conversations.append(reqCtx.ContextID, asking, agent.Message{Role: agent.RoleAssistant, Content: res.Text})
	memory.Said(uuid.NewString(), "assistant", res.Text)

	if err := stream.close(res.Text); err != nil {
		return err
	}
	return finish(ctx, queue, reqCtx, a2a.TaskStateCompleted, "")
}

func (e *executor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	return finish(ctx, queue, reqCtx, a2a.TaskStateCanceled, "")
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

func metadata(msg *a2a.Message) (string, map[string]string) {
	if msg == nil {
		return "", nil
	}
	subject, _ := msg.Metadata[SubjectKey].(string)
	raw, _ := msg.Metadata[MCPHeadersKey].(map[string]any)
	headers := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			headers[k] = s
		}
	}
	return strings.TrimSpace(subject), headers
}

// stream writes the turn to the task as it happens.
type stream struct {
	agent.NopCallback
	ctx    context.Context
	queue  eventqueue.Queue
	task   *a2asrv.RequestContext
	memory *recall.Recorder
	answer a2a.ArtifactID

	mu      sync.Mutex
	started bool
	// pending is the latest chunk, held back so the last one can be marked
	// lastChunk without an empty part after it.
	pending string
}

func (s *stream) OnTextDelta(text string) {
	if text == "" {
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

func (s *stream) OnToolEnd(trace agent.ToolCallTrace) {
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
