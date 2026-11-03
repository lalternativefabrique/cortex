package host

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/a2aproject/a2a-go/a2a"
)

// Ask runs one turn from inside the process, as a scheduled task does, and
// returns the answer's text. The turn runs for subject, in the
// conversation named by contextID, exactly as a message over /a2a would.
func (h *Host) Ask(ctx context.Context, subject, contextID, text string) (string, error) {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: text})
	msg.ContextID = contextID
	if subject != "" {
		msg.Metadata = map[string]any{SubjectKey: subject}
	}
	turn := Turn{TaskID: a2a.NewTaskID(), ContextID: contextID, Message: msg}

	var mu sync.Mutex
	var answer strings.Builder
	var failed string
	emit := func(_ context.Context, ev a2a.Event) error {
		mu.Lock()
		defer mu.Unlock()
		switch ev := ev.(type) {
		case *a2a.TaskArtifactUpdateEvent:
			if ev.Artifact == nil || ev.Artifact.Name != "answer" {
				return nil
			}
			for _, p := range ev.Artifact.Parts {
				if t, ok := p.(a2a.TextPart); ok {
					answer.WriteString(t.Text)
				}
			}
		case *a2a.TaskStatusUpdateEvent:
			if ev.Status.State == a2a.TaskStateFailed || ev.Status.State == a2a.TaskStateCanceled {
				failed = string(ev.Status.State)
				if ev.Status.Message != nil {
					failed += ": " + messageText(ev.Status.Message)
				}
			}
		}
		return nil
	}
	if err := h.turns.Dispatch(ctx, turn, emit); err != nil {
		return "", err
	}
	mu.Lock()
	defer mu.Unlock()
	if failed != "" {
		return "", errors.New(failed)
	}
	return answer.String(), nil
}
