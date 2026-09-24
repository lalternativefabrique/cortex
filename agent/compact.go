package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Compactor shrinks a conversation that no longer fits the context window.
type Compactor interface {
	// Compact returns a shorter history conveying the same working state.
	// The returned slice replaces the old one wholesale. The returned
	// *MemoryExtraction is what this call newly extracted, nil when nothing
	// was — the caller decides whether it is worth persisting past the run.
	//
	// Compacting costs a model call of its own, on the whole stretch being
	// summarised. Its Usage is returned so the run it belongs to accounts for
	// it: dropping it hides the spend of exactly the long conversations that
	// cost the most.
	Compact(ctx context.Context, system string, messages []Message) ([]Message, *MemoryExtraction, Usage, error)
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

Be specific — name files, functions and errors. Omit narration of which tools ran. Write prose, no preamble.

If the transcript contains decisions, constraints, or facts worth preserving exactly, call record_memory with them in addition to writing the summary.`

// memoryExtractionTool exists only to shape the schema offered to
// summarize's extraction call. It is never registered on a Runner and its
// Execute is never invoked — the compactor reads the tool call's arguments
// directly instead of dispatching it.
type memoryExtractionTool struct{}

func (memoryExtractionTool) Name() string { return "record_memory" }
func (memoryExtractionTool) Description() string {
	return "Record decisions, constraints and facts extracted from the transcript, so they survive without being re-summarised later."
}
func (memoryExtractionTool) InputSchema() any { return MemoryExtraction{} }
func (memoryExtractionTool) Execute(context.Context, json.RawMessage) (ToolResult, error) {
	return ToolResult{}, fmt.Errorf("memoryExtractionTool.Execute called: this tool is schema-only and must never run")
}

// Compact summarises the middle of the conversation, pinning a prior
// extraction (RoleMemory) verbatim rather than folding it back into the
// prose that gets summarised — otherwise a second compaction re-summarises
// an already-summarised text, and detail is lost a second time for free.
func (c *SummaryCompactor) Compact(ctx context.Context, system string, messages []Message) ([]Message, *MemoryExtraction, Usage, error) {
	keepRecent := c.KeepRecent
	if keepRecent <= 0 {
		keepRecent = DefaultKeepRecent
	}
	keepFirst := c.KeepFirst
	if keepFirst <= 0 {
		keepFirst = DefaultKeepFirst
	}

	if len(messages) <= keepFirst+keepRecent {
		return messages, nil, Usage{}, nil
	}

	head := messages[:keepFirst]
	body, pinned := extractPinned(messages[keepFirst : len(messages)-keepRecent])
	tail := messages[len(messages)-keepRecent:]

	summary, mem, usage, err := c.summarize(ctx, body)
	if err != nil {
		return nil, nil, Usage{}, err
	}

	out := make([]Message, 0, keepFirst+len(pinned)+2+len(tail))
	out = append(out, head...)
	out = append(out, pinned...)
	if mem != nil && !mem.IsEmpty() {
		out = append(out, Message{Role: RoleMemory, Content: mem.Render()})
	}
	out = append(out, Message{
		Role:    RoleUser,
		Content: "[Earlier turns were compacted to fit the context window. Summary of what happened:]\n\n" + summary,
	})
	// A tool message whose matching assistant turn was cut would reference a
	// tool_call_id the provider can no longer resolve, which most reject.
	out = append(out, dropOrphanToolMessages(tail)...)
	return out, mem, usage, nil
}

// extractPinned pulls prior RoleMemory messages out of a slice about to be
// summarised, so they are carried forward untouched instead of being fed
// back into another round of summarisation.
func extractPinned(messages []Message) (rest, pinned []Message) {
	rest = make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == RoleMemory {
			pinned = append(pinned, m)
			continue
		}
		rest = append(rest, m)
	}
	return rest, pinned
}

func (c *SummaryCompactor) summarize(ctx context.Context, middle []Message) (string, *MemoryExtraction, Usage, error) {
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
		Tools: []Tool{memoryExtractionTool{}},
	})
	if err != nil {
		return "", nil, Usage{}, fmt.Errorf("summarise transcript: %w", err)
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "", nil, resp.Usage, fmt.Errorf("summarise transcript: model returned nothing")
	}
	return resp.Text, extractMemory(resp.ToolCalls), resp.Usage, nil
}

// extractMemory reads the model's record_memory call, if it made one. The
// model is never forced to call it — no result here is normal, not an
// error, and Compact must succeed exactly as it did before this call was
// offered.
func extractMemory(calls []ToolCall) *MemoryExtraction {
	for _, tc := range calls {
		if tc.Name != "record_memory" {
			continue
		}
		var mem MemoryExtraction
		if err := json.Unmarshal(tc.Arguments, &mem); err != nil {
			return nil
		}
		return &mem
	}
	return nil
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
