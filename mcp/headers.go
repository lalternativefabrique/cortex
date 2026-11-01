package mcp

import "context"

type headersKey struct{}

// Headers are what the caller of one turn lends its tools over HTTP, such as
// an end user's grant: by server name, and under "*" for every server. The
// model never sees them, since they travel beside the tool call, not in it.
type Headers map[string]map[string]string

// WithHeaders returns a context whose MCP calls over HTTP carry headers.
func WithHeaders(ctx context.Context, headers Headers) context.Context {
	if len(headers) == 0 {
		return ctx
	}
	return context.WithValue(ctx, headersKey{}, headers)
}

// headersFor is what the server named receives: its own headers over those
// lent to every server.
func headersFor(ctx context.Context, server string) map[string]string {
	all, _ := ctx.Value(headersKey{}).(Headers)
	if len(all) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range all["*"] {
		out[k] = v
	}
	for k, v := range all[server] {
		out[k] = v
	}
	return out
}
