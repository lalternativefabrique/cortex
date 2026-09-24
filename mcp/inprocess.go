package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Handler is an MCP server that lives in the host's own process.
//
// An agent consumes MCP servers that are external to it — a subprocess over
// stdio, a service over HTTP — and servers that are internal to the system it
// is deployed in. A product that links cortex as a library owns its tools in
// the same binary: making it reach them over a socket would serialise a call
// that is already in memory, and add a listening port and a failure mode to
// something that cannot fail to connect.
//
// The protocol is the contract, not the transport. A Handler answers the same
// tools/list and tools/call as a remote server, so a tool offered this way is
// indistinguishable from one offered over HTTP — same prefixing, same schema,
// same error shape — and moving it out of the process later changes a
// ServerConfig, not the agent.
type Handler interface {
	// Tools are what the server offers, in the same shape tools/list returns.
	Tools() []ToolDescriptor
	// Call runs one tool. A tool-level failure is returned as a result with
	// IsError set, exactly as a remote server reports it; an error is for a
	// call that could not be attempted at all.
	Call(ctx context.Context, name string, arguments json.RawMessage) (ToolResult, error)
}

// ToolDescriptor describes one tool a Handler offers.
type ToolDescriptor struct {
	Name        string
	Description string
	// InputSchema is the tool's JSON Schema. Nil offers a tool that takes no
	// arguments.
	InputSchema json.RawMessage
}

// ToolResult is what one tool call produced.
type ToolResult struct {
	Text    string
	IsError bool
}

// inProcessTransport answers the client's JSON-RPC calls from a Handler,
// without a wire between them.
type inProcessTransport struct {
	handler Handler
}

func newInProcessTransport(h Handler) transport { return &inProcessTransport{handler: h} }

func (t *inProcessTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	switch method {
	case "initialize":
		return json.Marshal(map[string]any{"protocolVersion": ProtocolVersion})
	case "tools/list":
		return t.listTools()
	case "tools/call":
		return t.callTool(ctx, params)
	default:
		return nil, fmt.Errorf("unsupported method %q", method)
	}
}

// notify accepts what the handshake sends and does nothing with it: the
// notifications a remote server needs to sequence its startup have no
// meaning for a handler that was ready before the client existed.
func (t *inProcessTransport) notify(context.Context, string, any) error { return nil }

// close releases nothing: the handler's lifetime is the host's, and a client
// disconnecting is not a reason to tear down tools the host still owns.
func (t *inProcessTransport) close() error { return nil }

func (t *inProcessTransport) listTools() (json.RawMessage, error) {
	described := t.handler.Tools()
	out := make([]toolDescriptor, 0, len(described))
	for _, d := range described {
		schema := d.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, toolDescriptor{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: schema,
		})
	}
	return json.Marshal(listToolsResult{Tools: out})
}

func (t *inProcessTransport) callTool(ctx context.Context, params any) (json.RawMessage, error) {
	name, arguments, err := callParams(params)
	if err != nil {
		return nil, err
	}

	res, err := t.handler.Call(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	return json.Marshal(callToolResult{
		Content: []textContent{{Type: "text", Text: res.Text}},
		IsError: res.IsError,
	})
}

// callParams reads the name and arguments out of what the client sent. It
// round-trips through JSON rather than asserting the map's shape, so this
// transport reads exactly what a remote one would receive.
func callParams(params any) (string, json.RawMessage, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return "", nil, fmt.Errorf("encode params: %w", err)
	}
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return "", nil, fmt.Errorf("decode params: %w", err)
	}
	if call.Name == "" {
		return "", nil, fmt.Errorf("a tool name is required")
	}
	return call.Name, call.Arguments, nil
}

// ConnectHandler offers an in-process server's tools to the agent. The name
// prefixes them, exactly as it does for a remote server.
func ConnectHandler(ctx context.Context, name string, h Handler) ([]agent.Tool, error) {
	if name == "" {
		return nil, fmt.Errorf("mcp: a server name is required")
	}
	if h == nil {
		return nil, fmt.Errorf("mcp: a handler is required")
	}
	c := &Client{
		cfg: cfg{name: name, timeout: DefaultRequestTimeout},
		t:   newInProcessTransport(h),
	}
	if err := c.handshake(ctx); err != nil {
		return nil, err
	}
	return c.ListTools(ctx)
}
