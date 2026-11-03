package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// WithFallbacks returns a client that asks primary first and, when it fails,
// each fallback provider in turn, so a provider's outage does not end the
// run. A call the caller cancelled is not retried elsewhere, and a streamed
// answer that already reached the caller is not restarted on another model:
// the person would read it twice.
func WithFallbacks(primary Client, fallbacks ...Provider) (Client, error) {
	if len(fallbacks) == 0 {
		return primary, nil
	}
	f := &fallbackClient{clients: []Client{primary}, labels: []string{"primary"}}
	for _, p := range fallbacks {
		c, err := NewClient(p)
		if err != nil {
			return nil, fmt.Errorf("fallback provider: %w", err)
		}
		f.clients = append(f.clients, c)
		f.labels = append(f.labels, label(p))
	}
	return f, nil
}

type fallbackClient struct {
	clients []Client
	labels  []string
}

func (f *fallbackClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return f.each(ctx, func(c Client) (CompletionResponse, error) { return c.Complete(ctx, req) })
}

func (f *fallbackClient) CompleteStream(ctx context.Context, req CompletionRequest, onDelta func(string)) (CompletionResponse, error) {
	return f.each(ctx, func(c Client) (CompletionResponse, error) {
		streamed := false
		watch := func(s string) {
			streamed = true
			onDelta(s)
		}
		sc, ok := c.(StreamingClient)
		if !ok {
			return c.Complete(ctx, req)
		}
		resp, err := sc.CompleteStream(ctx, req, watch)
		if err != nil && streamed {
			return resp, errFinal{err}
		}
		return resp, err
	})
}

// errFinal marks an error no other provider should be asked about.
type errFinal struct{ err error }

func (e errFinal) Error() string { return e.err.Error() }
func (e errFinal) Unwrap() error { return e.err }

func (f *fallbackClient) each(ctx context.Context, call func(Client) (CompletionResponse, error)) (CompletionResponse, error) {
	var failures []string
	for i, c := range f.clients {
		resp, err := call(c)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return resp, err
		}
		var final errFinal
		if errors.As(err, &final) {
			return resp, final.err
		}
		failures = append(failures, f.labels[i]+": "+err.Error())
		if i+1 < len(f.clients) {
			slog.Warn("agent: model failed over", "from", f.labels[i], "to", f.labels[i+1], "error", err)
		}
	}
	return CompletionResponse{}, fmt.Errorf("every model failed: %s", strings.Join(failures, "; "))
}

func label(p Provider) string {
	host := p.BaseURL
	if u, err := url.Parse(p.BaseURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return p.Model + "@" + host
}
