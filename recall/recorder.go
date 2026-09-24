package recall

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lalternative/packages/go/cortex/agent"
)

// maxDidBytes keeps a remembered action to what it was and how it ended: a
// tool's full output belongs to the run, not to memory.
const maxDidBytes = 600

// Recorder writes what the agent does while it runs. Pass it as, or chain it
// into, the run's agent.Callback; call Said for the turns the host owns.
//
// Remembering is best-effort: a store that fails is logged and the run goes
// on, since losing a memory must never lose the answer.
type Recorder struct {
	agent.NopCallback
	ctx          context.Context
	store        Store
	scope        Scope
	conversation string
}

func NewRecorder(ctx context.Context, store Store, scope Scope, conversation string) *Recorder {
	return &Recorder{ctx: context.WithoutCancel(ctx), store: store, scope: scope, conversation: conversation}
}

// Said remembers a user or assistant turn, under the host's own message id.
func (r *Recorder) Said(message, role, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	r.remember(Entry{Message: message, Kind: Said, Role: role, Content: content})
}

// Produced remembers what a run delivered: a summary, a diff, a pull request.
func (r *Recorder) Produced(message, content string) {
	r.remember(Entry{Message: message, Kind: Produced, Role: "assistant", Content: content})
}

func (r *Recorder) OnToolEnd(trace agent.ToolCallTrace) {
	if trace.Name == toolName {
		return
	}
	outcome := trace.Result
	if trace.Err != "" {
		outcome = "failed: " + trace.Err
	}
	r.remember(Entry{
		Message: uuid.NewString(),
		Kind:    Did,
		Role:    trace.Name,
		Content: truncate(fmt.Sprintf("%s(%s) -> %s", trace.Name, trace.Arguments, outcome), maxDidBytes),
	})
}

func (r *Recorder) remember(e Entry) {
	if r == nil || r.store == nil {
		return
	}
	e.Scope = r.scope
	e.Conversation = r.conversation
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if err := r.store.Remember(r.ctx, e); err != nil {
		slog.Warn("recall: memory not written", "agent", r.scope.Agent, "conversation", r.conversation, "kind", e.Kind, "error", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
