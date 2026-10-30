package mcp

import "context"

type headersKey struct{}

// WithHeaders returns a context whose MCP calls over HTTP carry headers: what
// the caller of one turn lends its tools, such as an end user's grant. The
// model never sees them, since they travel beside the tool call, not in it.
func WithHeaders(ctx context.Context, headers map[string]string) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, headersKey{}, headers)
}

func headersFrom(ctx context.Context) map[string]string {
	h, _ := ctx.Value(headersKey{}).(map[string]string)
	return h
}
