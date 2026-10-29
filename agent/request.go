package agent

import (
	"context"
	"sync"
)

type personRequestKey struct{}

type personRequest struct {
	mu   sync.Mutex
	text string
}

func withPersonRequest(ctx context.Context, messages []Message) context.Context {
	req := &personRequest{}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser && messages[i].Content != "" {
			req.text = messages[i].Content
			break
		}
	}
	return context.WithValue(ctx, personRequestKey{}, req)
}

func setPersonRequest(ctx context.Context, text string) {
	if req, ok := ctx.Value(personRequestKey{}).(*personRequest); ok {
		req.mu.Lock()
		req.text = text
		req.mu.Unlock()
	}
}

// PersonRequest is what the person last asked in the turn ctx belongs to,
// a message written mid-turn included, so a gate can judge an action
// against it. Outside a turn it is empty.
func PersonRequest(ctx context.Context) string {
	req, ok := ctx.Value(personRequestKey{}).(*personRequest)
	if !ok {
		return ""
	}
	req.mu.Lock()
	defer req.mu.Unlock()
	return req.text
}

// WithPersonRequest sets what the person asked, for a gate run outside a
// turn of this loop, such as a host judging an action of its own.
func WithPersonRequest(ctx context.Context, text string) context.Context {
	return withPersonRequest(ctx, []Message{{Role: RoleUser, Content: text}})
}
