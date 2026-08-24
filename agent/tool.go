// Package agent implements a coding-agent loop over an OpenAI-compatible
// chat endpoint: the model alternates between requesting tools and emitting
// text until it produces a final answer or the step budget is exhausted.
//
// The package is transport-agnostic above the Client interface and emits
// progress through Callback, so the same loop drives a CLI, a service, or a
// test harness without change.
package agent

import (
	"context"
	"encoding/json"
)

// Tool is a capability exposed to the model.
//
// Implementations MUST honor ctx.Done() and return ctx.Err() promptly when
// cancelled: the loop relies on this to abort in-flight tools when the
// session is closed or a timeout fires.
//
// InputSchema returns a Go value — typically a zero struct — annotated with
// `jsonschema:"..."` tags. The runtime converts it to JSON Schema and ships
// it to the provider.
//
// Execute reserves its error return for failures that must abort the run.
// Anything the model can recover from — a missing file, a non-zero exit, a
// malformed argument — belongs in ToolResult.Content as a readable message,
// so the model can correct itself instead of the loop dying.
type Tool interface {
	Name() string
	Description() string
	InputSchema() any
	Execute(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// ToolResult is the output of a Tool.Execute call.
//
// Content is fed back to the model and costs tokens. Metadata stays
// runtime-side: it reaches Callback for audit, logging and metrics, and is
// never injected into the model context.
type ToolResult struct {
	Content  string
	Metadata map[string]any
}

// Role identifies the author of a Message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in the conversation history.
//
// ToolCallID is set when Role is RoleTool and references the assistant turn
// that requested the call. ToolCalls is set when Role is RoleAssistant and
// the model decided to invoke one or more tools.
type Message struct {
	Role       Role
	Content    string
	ToolCallID string
	ToolCalls  []ToolCall
}

// ToolCall is a tool invocation requested by the model.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage reports token consumption, summed across every model call in a run.
//
// CachedInput counts prompt-cache hits. It is the signal that the stable
// prefix is holding: a run whose CachedInput stays near zero after the first
// step has a prefix churning somewhere.
type Usage struct {
	Input       int
	Output      int
	CachedInput int
}

// Add accumulates another Usage into u.
func (u *Usage) Add(other Usage) {
	u.Input += other.Input
	u.Output += other.Output
	u.CachedInput += other.CachedInput
}

// ToolCallTrace records what happened during a single tool invocation.
//
// Err is stored as a string so a trace survives serialization to JSONB or a
// message bus. An empty Err means the tool succeeded.
type ToolCallTrace struct {
	Name       string
	Arguments  string
	Result     string
	DurationMs int64
	Err        string
}

// Result is the outcome of a Run.
type Result struct {
	Text      string
	ToolCalls []ToolCallTrace
	Usage     Usage
	Steps     int
	// Truncated reports that the loop stopped on the step budget rather than
	// on a final answer from the model.
	Truncated bool
	// Compactions counts how many times the history was summarised.
	Compactions int
	// Recoveries counts turns where the model wrote a tool call as text and
	// was asked to retry.
	Recoveries int
}
