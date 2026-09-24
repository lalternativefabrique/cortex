package serve

import (
	"context"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/promptctx"
)

const a2aPath = "/a2a"

// executor runs one A2A task as a cortex turn. The protocol's vocabulary maps
// onto what the server already holds: a Task is a turn, and a ContextID is
// the conversation it belongs to, which is the same identity /turn tracks as
// a session.
type executor struct {
	srv *Server
}

var _ a2asrv.AgentExecutor = (*executor)(nil)

// Execute answers one task. It reports the states the protocol expects —
// submitted on a new task, working while the model runs, completed or failed
// at the end — so a client watching the stream sees the run progress rather
// than a silence followed by an answer.
func (e *executor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	if reqCtx.StoredTask == nil {
		if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateSubmitted, nil)); err != nil {
			return fmt.Errorf("write submitted: %w", err)
		}
	}

	asked := messageText(reqCtx.Message)
	if asked == "" {
		return e.fail(ctx, reqCtx, queue, fmt.Errorf("a message is required"))
	}

	if err := queue.Write(ctx, a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateWorking, nil)); err != nil {
		return fmt.Errorf("write working: %w", err)
	}

	res, err := e.srv.runTask(ctx, reqCtx.ContextID, asked)
	if err != nil {
		return e.fail(ctx, reqCtx, queue, err)
	}

	answer := a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: res.Text})
	if err := queue.Write(ctx, answer); err != nil {
		return fmt.Errorf("write answer: %w", err)
	}
	return queue.Write(ctx, finalStatus(reqCtx, a2a.TaskStateCompleted, nil))
}

// Cancel stops a task. The run itself is bound to the request context, which
// the handler cancels; what is left is to tell the client the task ended
// because it was asked to, not because it finished.
func (e *executor) Cancel(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue) error {
	return queue.Write(ctx, finalStatus(reqCtx, a2a.TaskStateCanceled, nil))
}

// fail reports a failed run as a task state rather than as a transport error:
// the client asked a question and is owed an answer about it, and a protocol
// error would say the request itself was malformed.
func (e *executor) fail(ctx context.Context, reqCtx *a2asrv.RequestContext, queue eventqueue.Queue, cause error) error {
	msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, reqCtx, a2a.TextPart{Text: cause.Error()})
	return queue.Write(ctx, finalStatus(reqCtx, a2a.TaskStateFailed, msg))
}

// finalStatus marks the event as the last of its task: a2a-go closes the
// sequence on that flag, not on the state, and a client blocked on
// message/send hangs without it.
func finalStatus(reqCtx *a2asrv.RequestContext, state a2a.TaskState, msg *a2a.Message) *a2a.TaskStatusUpdateEvent {
	ev := a2a.NewStatusUpdateEvent(reqCtx, state, msg)
	ev.Final = true
	return ev
}

// messageText flattens a message's parts to the text the agent is asked. A
// part the agent cannot read is skipped rather than rendered as its type: a
// turn that says "[file]" to the model is worse than one that says nothing.
func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range msg.Parts {
		text, ok := p.(a2a.TextPart)
		if !ok {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(text.Text)
	}
	return strings.TrimSpace(b.String())
}

// runTask runs one turn on the conversation named by contextID, which is what
// makes an A2A context a session: a second task under the same ContextID
// continues the history the first one built.
func (e *Server) runTask(ctx context.Context, contextID, asked string) (agent.Result, error) {
	client, err := agent.NewClient(e.cfg.Provider)
	if err != nil {
		return agent.Result{}, err
	}

	_, conv := e.conversation(TurnRequest{Conversation: contextID})

	e.mu.Lock()
	if len(conv.history) == 0 {
		conv.history = append(conv.history,
			agent.Message{Role: agent.RoleUser, Content: promptctx.Workspace(e.cfg.Root)},
			agent.Message{Role: agent.RoleAssistant, Content: "Noted. What would you like to do?"},
		)
	}
	asking := agent.Message{Role: agent.RoleUser, Content: asked}
	conv.history = append(conv.history, asking)
	if conv.store != nil {
		_ = conv.store.Append(asking)
	}
	history := append([]agent.Message(nil), conv.history...)
	e.mu.Unlock()

	runner, err := agent.NewRunner(agent.Config{
		Client:        client,
		Tools:         e.tools,
		System:        e.system(""),
		MaxSteps:      e.cfg.MaxSteps,
		ContextWindow: e.cfg.ContextWindow,
	})
	if err != nil {
		return agent.Result{}, err
	}

	res, err := runner.Run(ctx, history)
	if err != nil {
		return agent.Result{}, err
	}

	e.mu.Lock()
	answered := agent.Message{Role: agent.RoleAssistant, Content: res.Text}
	conv.history = append(conv.history, answered)
	if conv.store != nil {
		_ = conv.store.Append(answered)
	}
	e.mu.Unlock()
	return res, nil
}

// a2aHandler builds the protocol surface: the SDK owns the JSON-RPC framing,
// the task store and the streaming, and calls the executor for the one thing
// it cannot know, which is what this agent does with a message.
func (s *Server) a2aHandler() a2asrv.RequestHandler {
	return a2asrv.NewHandler(&executor{srv: s})
}
