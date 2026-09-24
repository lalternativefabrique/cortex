// Package mcp speaks the Model Context Protocol, so tools provided by
// external servers — a database, an issue tracker, a browser — can be handed
// to the agent alongside the built-in ones.
//
// Two transports are supported. A local server (ServerConfig.Command set)
// is JSON-RPC 2.0 over stdio: the client spawns it as a subprocess and
// exchanges newline-delimited JSON on its pipes. A remote server
// (ServerConfig.URL set) is JSON-RPC 2.0 over HTTP: one POST per call,
// stateless, matching digstack/synthiz's apps/core/mcpserver — the only HTTP
// MCP server in the group as of this package's writing. Exactly one of
// Command or URL must be set; the transport built from a ServerConfig is
// otherwise identical to its caller, since everything above call/notify in
// this package (tool.go, config.go) never touches the transport directly.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ProtocolVersion is the revision this client implements. A server that
// answers with a different one still works for the tools/* calls used here;
// the field is sent because the handshake requires it.
const ProtocolVersion = "2024-11-05"

// ServerConfig describes how to reach one MCP server — exactly one of
// Command (stdio, a local subprocess) or URL (HTTP, a remote server) must be
// set.
type ServerConfig struct {
	Name string `yaml:"name" json:"name"`

	// Command, Args and Env launch a local server over stdio. Command is
	// empty for a remote server.
	Command string            `yaml:"command" json:"command"`
	Args    []string          `yaml:"args" json:"args"`
	Env     map[string]string `yaml:"env" json:"env"`

	// URL reaches a remote server over HTTP: one POST per JSON-RPC call,
	// stateless (see synthiz's apps/core/mcpserver). Empty for a local
	// server.
	//
	// Deliberately the only field an HTTP server carries here — no header,
	// no bearer token. synthiz's own server takes no transport-level auth at
	// all: whatever a tool call needs to authenticate as (a grant, a scoped
	// token) is a tool ARGUMENT the caller supplies per call, not something
	// this generic transport understands. A registry entry that wanted to
	// inject a fixed secret into every call would defeat the point of a
	// grant that expires in five minutes.
	URL string `yaml:"url" json:"url"`

	// Timeout caps a single request. Zero means DefaultRequestTimeout.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
	// Stderr receives a local server's diagnostics. Nil sends them to the
	// process's own stderr, which suits a CLI; a service handling several
	// runs at once wants them separated per run. Unused for a remote server.
	Stderr io.Writer `yaml:"-" json:"-"`
}

const DefaultRequestTimeout = 60 * time.Second

type request struct {
	JSONRPC string `json:"jsonrpc"`
	// ID is a pointer so a notification (Method with no reply expected) can
	// omit it entirely — omitempty on an int64 would instead send 0, which
	// synthiz's server (and the spec) reads as a real request id.
	ID     *int64 `json:"id,omitempty"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message) }

// transport is what a Client needs from either wire format: send a request
// expecting a reply, send one that expects none, and shut down. Nothing
// above this in the package (tool.go, config.go) knows which one it is
// talking to.
type transport interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
	notify(ctx context.Context, method string, params any) error
	close() error
}

// Client is a connection to one MCP server, over whichever transport its
// ServerConfig named.
type Client struct {
	cfg cfg
	t   transport
}

// cfg is the subset of ServerConfig a Client still needs after connecting —
// kept separate so transport-specific fields (Command, Args, Env, URL) stay
// out of code that only ever wants the name.
type cfg struct {
	name    string
	timeout time.Duration
}

// Connect reaches the server — spawning it for a local (Command) config,
// dialing it for a remote (URL) one — and completes the initialize
// handshake.
func Connect(ctx context.Context, sc ServerConfig) (*Client, error) {
	if sc.Command == "" && sc.URL == "" {
		return nil, errors.New("mcp: one of Command or URL is required")
	}
	if sc.Command != "" && sc.URL != "" {
		return nil, errors.New("mcp: Command and URL are mutually exclusive")
	}
	if sc.Timeout == 0 {
		sc.Timeout = DefaultRequestTimeout
	}

	var t transport
	var err error
	if sc.Command != "" {
		t, err = newStdioTransport(sc)
	} else {
		t = newHTTPTransport(sc)
	}
	if err != nil {
		return nil, err
	}

	c := &Client{cfg: cfg{name: sc.Name, timeout: sc.Timeout}, t: t}
	if err := c.handshake(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "skode", "version": "0.1.0"},
	})
	if err != nil {
		return fmt.Errorf("mcp %s: initialize: %w", c.cfg.name, err)
	}
	// The spec requires this notification before any other request; servers
	// that enforce it reject tools/list without it.
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, c.cfg.timeout)
	defer cancel()
	raw, err := c.t.call(cctx, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %s: %w", c.cfg.name, method, err)
	}
	return raw, nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	if err := c.t.notify(ctx, method, params); err != nil {
		return fmt.Errorf("mcp %s: %s: %w", c.cfg.name, method, err)
	}
	return nil
}

// Close shuts the connection down — killing the subprocess for a local
// server, releasing the HTTP client's resources for a remote one.
func (c *Client) Close() error { return c.t.close() }

// Name is the server's configured name.
func (c *Client) Name() string { return c.cfg.name }
