package serve

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

const journalTimeout = 10 * time.Second

// Journal keeps the account's history of finished turns. It is called once
// per turn, off the turn's path: an error is logged and never reaches the
// person.
type Journal interface {
	Record(ctx context.Context, entry JournalEntry) error
}

// JournalEntry is one finished turn.
type JournalEntry struct {
	Conversation string
	Turn         string
	Question     string
	Answer       string
	// TitleHint is the conversation's first question, set only on the turn
	// that opened it.
	TitleHint string
	ToolCalls []JournalToolCall
	// Err is set when the turn ended without an answer: a failure or a
	// cancellation.
	Err string
	At  time.Time
}

// JournalToolCall is one action of a turn: which tool, on what, how it ended.
type JournalToolCall struct {
	Tool    string
	Target  string
	Outcome string
	Err     string
}

const (
	OutcomeOK    = "ok"
	OutcomeError = "error"
)

const maxJournalErr = 300

var targetKeys = []string{"path", "file_path", "url", "command", "query", "pattern", "location", "name"}

func journalToolCall(trace agent.ToolCallTrace) JournalToolCall {
	call := JournalToolCall{Tool: trace.Name, Target: toolTarget(trace.Arguments), Outcome: OutcomeOK}
	if failed := toolFailure(trace); failed != "" {
		call.Outcome = OutcomeError
		call.Err = truncateText(failed, maxJournalErr)
	}
	return call
}

// toolFailure reads a failure the way tools report it: most return it as an
// "error: ..." result marked ok=false rather than as a Go error, so the model
// can correct itself.
func toolFailure(trace agent.ToolCallTrace) string {
	if trace.Err != "" {
		return trace.Err
	}
	refused := false
	if ok, set := trace.Metadata["ok"].(bool); set && !ok {
		refused = true
	}
	if refused || strings.HasPrefix(trace.Result, "error: ") {
		if msg := strings.TrimSpace(strings.TrimPrefix(trace.Result, "error: ")); msg != "" {
			return msg
		}
		return "failed"
	}
	return ""
}

func toolTarget(arguments string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return truncateText(arguments, maxJournalErr)
	}
	for _, key := range targetKeys {
		if v, ok := args[key].(string); ok && v != "" {
			return truncateText(v, maxJournalErr)
		}
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v, ok := args[k].(string); ok && v != "" {
			return truncateText(v, maxJournalErr)
		}
	}
	return ""
}

func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *Server) journal(ctx context.Context, entry JournalEntry) {
	if s.cfg.Journal == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), journalTimeout)
		defer cancel()
		if err := s.cfg.Journal.Record(ctx, entry); err != nil {
			slog.Warn("serve: turn not journaled", "conversation_id", entry.Conversation, "turn", entry.Turn, "error", err)
		}
	}()
}
