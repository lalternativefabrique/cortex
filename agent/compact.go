package agent

import (
	"context"
	"fmt"
	"strings"
)

// Compactor shrinks a conversation that no longer fits the context window.
type Compactor interface {
	// Compact returns a shorter history conveying the same working state.
	// The returned slice replaces the old one wholesale.
	Compact(ctx context.Context, system string, messages []Message) ([]Message, error)
}

// SummaryCompactor replaces the older turns with a model-written summary and
// keeps the most recent ones verbatim.
//
// Compaction is deliberately rare rather than continuous. Rewriting the
// middle of the prompt invalidates the inference server's cached prefix from
// that point on, so every compaction costs a full re-read of what follows.
// Waiting until the window is genuinely tight pays that price once instead
// of repeatedly.
type SummaryCompactor struct {
	// Client runs the summarisation call. It may be the same client the loop
	// uses, or a cheaper model.
	Client Client
	// KeepRecent is how many trailing messages survive verbatim. The most
	// recent turns are where the actual work is; summarising them loses the
	// detail the model is actively using.
	KeepRecent int
	// KeepFirst is how many leading messages survive verbatim. The opening
	// user message states the task, and losing it lets the agent drift.
	KeepFirst int
}

const (
	DefaultKeepRecent = 6
	DefaultKeepFirst  = 1
)

const summaryInstruction = `You are compacting an agent transcript so work can continue in a smaller context window.

Write a summary that lets the agent resume without re-reading what was cut. Cover:
- the task as originally stated
- what has been established about the codebase: files, symbols, structure that matter
- what has been changed so far, and where
- what was tried and did not work, so it is not retried
- what remains to be done

Be specific — name files, functions and errors. Omit narration of which tools ran. Write prose, no preamble.`

// Compact summarises the middle of the conversation.
func (c *SummaryCompactor) Compact(ctx context.Context, system string, messages []Message) ([]Message, error) {
	keepRecent := c.KeepRecent
	if keepRecent <= 0 {
		keepRecent = DefaultKeepRecent
	}
	keepFirst := c.KeepFirst
	if keepFirst <= 0 {
		keepFirst = DefaultKeepFirst
	}

	if len(messages) <= keepFirst+keepRecent {
		return messages, nil
	}

	head := messages[:keepFirst]
	middle := messages[keepFirst : len(messages)-keepRecent]
	tail := messages[len(messages)-keepRecent:]

	summary, err := c.summarize(ctx, middle)
	if err != nil {
		return nil, err
	}

	out := make([]Message, 0, keepFirst+1+len(tail))
	out = append(out, head...)
	out = append(out, Message{
		Role:    RoleUser,
		Content: "[Earlier turns were compacted to fit the context window. Summary of what happened:]\n\n" + summary,
	})
	// A tool message whose matching assistant turn was cut would reference a
	// tool_call_id the provider can no longer resolve, which most reject.
	out = append(out, dropOrphanToolMessages(tail)...)
	return out, nil
}

func (c *SummaryCompactor) summarize(ctx context.Context, middle []Message) (string, error) {
	var b strings.Builder
	for _, m := range middle {
		switch m.Role {
		case RoleUser:
			fmt.Fprintf(&b, "USER: %s\n\n", m.Content)
		case RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&b, "CALLED %s(%s)\n", tc.Name, truncateForSummary(string(tc.Arguments)))
			}
			b.WriteByte('\n')
		case RoleTool:
			fmt.Fprintf(&b, "RESULT: %s\n\n", truncateForSummary(m.Content))
		}
	}

	resp, err := c.Client.Complete(ctx, CompletionRequest{
		System: summaryInstruction,
		Messages: []Message{{
			Role:    RoleUser,
			Content: "Transcript to summarise:\n\n" + b.String(),
		}},
	})
	if err != nil {
		return "", fmt.Errorf("summarise transcript: %w", err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "", fmt.Errorf("summarise transcript: model returned nothing")
	}
	return resp.Text, nil
}

// dropOrphanToolMessages removes leading tool messages whose assistant turn
// is no longer present.
func dropOrphanToolMessages(messages []Message) []Message {
	known := map[string]struct{}{}
	out := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == RoleAssistant {
			for _, tc := range m.ToolCalls {
				known[tc.ID] = struct{}{}
			}
		}
		if m.Role == RoleTool {
			if _, ok := known[m.ToolCallID]; !ok {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func truncateForSummary(s string) string {
	const max = 1500
	if len(s) <= max {
		return s
	}
	return s[:max] + " ...[cut]"
}
