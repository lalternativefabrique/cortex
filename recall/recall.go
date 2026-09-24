// Package recall is what an agent remembers across conversations: what was
// said, what it did and what it produced. The host decides where memories
// live by handing a Store; this package only defines the contract, the
// recall_memory tool, and a Recorder that writes as the agent works.
//
// It is not package memory, which keeps what compaction extracts within one
// run.
package recall

import (
	"context"
	"time"
)

// Kind is what a memory records.
type Kind string

const (
	Said     Kind = "said"
	Did      Kind = "did"
	Produced Kind = "produced"
)

// Scope is whose memory it is: one subject with one agent. A Store never
// returns a memory outside the scope it is asked about.
type Scope struct {
	Subject string
	Agent   string
}

type Entry struct {
	Scope
	Conversation string
	// Message identifies the entry within its conversation; remembering the
	// same message twice keeps one entry.
	Message string
	Kind    Kind
	// Role is who said it (user, assistant) for Said, and the tool for Did.
	Role    string
	Content string
	At      time.Time
}

type Store interface {
	Remember(ctx context.Context, e Entry) error
	Recall(ctx context.Context, s Scope, query string, limit int) ([]Entry, error)
	// Forget drops every memory of one conversation, when the host deletes it.
	Forget(ctx context.Context, s Scope, conversation string) error
}
