package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type listToolsResult struct {
	Tools []toolDescriptor `json:"tools"`
}

type callToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// ListTools returns the server's tools, adapted to the agent's interface.
//
// Names are prefixed with the server name, both to avoid collisions between
// servers and to make it obvious in a transcript where a capability came
// from.
func (c *Client) ListTools(ctx context.Context) ([]agent.Tool, error) {
	raw, err := c.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result listToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: decode tools/list: %w", c.cfg.Name, err)
	}

	out := make([]agent.Tool, 0, len(result.Tools))
	for _, d := range result.Tools {
		out = append(out, &remoteTool{client: c, descriptor: d})
	}
	return out, nil
}

type remoteTool struct {
	client     *Client
	descriptor toolDescriptor
}

func (t *remoteTool) Name() string {
	return t.client.cfg.Name + "__" + t.descriptor.Name
}

func (t *remoteTool) Description() string {
	desc := strings.TrimSpace(t.descriptor.Description)
	if desc == "" {
		desc = "No description was provided by the server."
	}
	return fmt.Sprintf("%s\n\n(Provided by the %q MCP server.)", desc, t.client.cfg.Name)
}

// InputSchema returns the schema the server published.
//
// rawSchema carries it through the runtime unchanged: re-deriving it from a
// Go type would lose whatever the server actually declared, and the server
// is the only authority on what its own tool accepts.
func (t *remoteTool) InputSchema() any {
	if len(t.descriptor.InputSchema) == 0 {
		return rawSchema(json.RawMessage(`{"type":"object","properties":{}}`))
	}
	return rawSchema(t.descriptor.InputSchema)
}

func (t *remoteTool) Execute(ctx context.Context, args json.RawMessage) (agent.ToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var arguments any
	if err := json.Unmarshal(args, &arguments); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: could not parse arguments: %v", err),
		}, nil
	}

	raw, err := t.client.call(ctx, "tools/call", map[string]any{
		"name":      t.descriptor.Name,
		"arguments": arguments,
	})
	if err != nil {
		// A server-side failure is something the model can react to — a bad
		// query, a missing record — so it comes back as a result rather than
		// ending the run.
		return agent.ToolResult{
			Content:  "error: " + err.Error(),
			Metadata: map[string]any{"ok": false, "server": t.client.cfg.Name},
		}, nil
	}

	var result callToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: could not decode the server's reply: %v", err),
		}, nil
	}

	var b strings.Builder
	for _, part := range result.Content {
		if part.Text == "" {
			// Image and resource parts have no text form; say so rather than
			// emitting nothing and leaving the model to guess.
			fmt.Fprintf(&b, "[%s content omitted: this harness handles text only]\n", part.Type)
			continue
		}
		b.WriteString(part.Text)
		b.WriteByte('\n')
	}
	content := strings.TrimRight(b.String(), "\n")
	if content == "" {
		content = "(the tool returned no content)"
	}
	if result.IsError {
		content = "error: " + content
	}

	return agent.ToolResult{
		Content: content,
		Metadata: map[string]any{
			"ok":     !result.IsError,
			"server": t.client.cfg.Name,
			"tool":   t.descriptor.Name,
		},
	}, nil
}

// rawSchema is a JSON Schema that is already encoded, passed through the
// runtime without being re-derived.
type rawSchema json.RawMessage

func (r rawSchema) MarshalJSON() ([]byte, error) { return r, nil }
