package ui

import "time"

// Kind identifies what happened. The set is closed: a front end switches on
// it exhaustively, and a transport serialises it as a string.
type Kind string

const (
	TurnStart     Kind = "turn.start"
	TurnEnd       Kind = "turn.end"
	StepStart     Kind = "step.start"
	TextDelta     Kind = "text.delta"
	ToolStart     Kind = "tool.start"
	ToolEnd       Kind = "tool.end"
	CommandOutput Kind = "command.output"
	Notice        Kind = "notice"
	Failure       Kind = "failure"
	UsageUpdate   Kind = "usage"
)

// Event is one thing the agent did, in terms a front end can render without
// knowing how the agent works.
//
// It carries no terminal types and no agent types: the same value feeds a
// TUI, a JSON stream over HTTP, or a test. Fields are flat and JSON-tagged so
// putting an API in front of this costs a transport and nothing else.
type Event struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`

	Step int    `json:"step,omitempty"`
	Text string `json:"text,omitempty"`

	Tool     string `json:"tool,omitempty"`
	Args     string `json:"args,omitempty"`
	Result   string `json:"result,omitempty"`
	Duration string `json:"duration,omitempty"`
	Failed   bool   `json:"failed,omitempty"`

	Usage *Usage `json:"usage,omitempty"`
}

// Usage is what a turn has consumed so far.
type Usage struct {
	Input       int    `json:"input"`
	CachedInput int    `json:"cachedInput"`
	Output      int    `json:"output"`
	Cost        string `json:"cost,omitempty"`
}

// Sink receives events. A front end implements it; so does a transport.
type Sink interface {
	Emit(Event)
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(Event)

func (f SinkFunc) Emit(e Event) { f(e) }

// Approval is a question the agent needs answered before it acts.
type Approval struct {
	Scope    string `json:"scope"`
	Question string `json:"question"`
	Detail   string `json:"detail,omitempty"`
}

// Answer is what came back.
type Answer string

const (
	Allow       Answer = "allow"
	Deny        Answer = "deny"
	AlwaysAllow Answer = "always"
)

// Asker decides an approval. The terminal implements it by asking; a server
// implements it by round-tripping to a client.
type Asker interface {
	Ask(Approval) Answer
}
