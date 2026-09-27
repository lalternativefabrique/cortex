package host

import (
	"context"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
)

type turnTokenKey struct{}

func withTurnToken(ctx context.Context, msg *a2a.Message) context.Context {
	if msg == nil {
		return ctx
	}
	token, _ := msg.Metadata[TurnTokenKey].(string)
	return WithTurnToken(ctx, token)
}

// WithTurnToken lends token to the calls made under the returned context.
// An empty token lends nothing.
func WithTurnToken(ctx context.Context, token string) context.Context {
	if token = strings.TrimSpace(token); token == "" {
		return ctx
	}
	return context.WithValue(ctx, turnTokenKey{}, token)
}

// TurnToken is the Bearer the caller lent the turn running under ctx, empty
// when it lent none. A transport of the model or memory client presents it in
// place of the configured key.
func TurnToken(ctx context.Context) string {
	token, _ := ctx.Value(turnTokenKey{}).(string)
	return token
}
