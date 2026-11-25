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

// SummaryCompactor replaces the older turns with a Handoff and keeps the
// most recent ones verbatim.
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
	// Deprecated: the task is found by Message.Task, and it and every later
	// instruction are carried verbatim in the handoff. KeepFirst is ignored.
	KeepFirst int
	// WorkState fills the handoff's repository section. Nil leaves it out.
	WorkState WorkState
}

const (
	DefaultKeepRecent = 6
	// Deprecated: see SummaryCompactor.KeepFirst.
	DefaultKeepFirst = 1
)

const (
	maxSummarySystem = 4000
	maxSummaryFailed = 4000
	maxSummaryOther  = 1500
)

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

// Compact replaces everything before the recent turns with one user
// message: the rendered Handoff. The recent turns it keeps start on an
// assistant message, so no two user messages ever follow each other.
func (c *SummaryCompactor) Compact(ctx context.Context, system string, messages []Message) ([]Message, *MemoryExtraction, Usage, error) {
	keepRecent := c.KeepRecent
	if keepRecent <= 0 {
		keepRecent = DefaultKeepRecent
	}
	if len(messages) <= keepRecent+1 {
		return messages, nil, Usage{}, nil
	}
	cut := tailStart(messages, len(messages)-keepRecent)
	h, mem, usage, err := c.Handoff(ctx, system, messages[:cut])
	if err != nil {
		return nil, nil, usage, err
	}
	out := make([]Message, 0, 1+len(messages)-cut)
	out = append(out, Message{Role: RoleUser, Content: h.Render(), Handoff: &h})
	// A tool message whose matching assistant turn was cut would reference a
	// tool_call_id the provider can no longer resolve, which most reject.
	out = append(out, dropOrphanToolMessages(messages[cut:])...)
	return out, mem, usage, nil
}

func tailStart(messages []Message, from int) int {
	for i := max(from, 0); i < len(messages); i++ {
		if messages[i].Role == RoleAssistant {
			return i
		}
	}
	return len(messages)
}

// Handoff writes the document a different agent takes the work over from.
// The task, the instructions received since and the pinned memory are
// copied, never summarised; the model writes the rest from the transcript.
func (c *SummaryCompactor) Handoff(ctx context.Context, system string, messages []Message) (Handoff, *MemoryExtraction, Usage, error) {
	h, prior, transcript := collectFacts(messages)
	if c.WorkState != nil {
		if repo, err := c.WorkState.State(ctx); err == nil {
			h.Repo = &repo
		}
	}
	text, mem, usage, err := c.summarize(ctx, system, h, prior, transcript)
	if err != nil {
		return Handoff{}, nil, usage, err
	}
	written := parseModelSections(text)
	written.Task, written.Instructions, written.Repo = h.Task, h.Instructions, h.Repo
	written.Memory = h.Memory
	if mem != nil {
		written.Memory = mergeMemory(written.Memory, *mem)
	}
	return written, mem, usage, nil
}

// collectFacts separates what is copied verbatim from what is summarised.
// The task is the message marked Task, or the first user message when none
// is marked; a prior handoff carries the task and instructions it held.
func collectFacts(messages []Message) (h Handoff, prior *Handoff, transcript []Message) {
	marked := false
	for _, m := range messages {
		marked = marked || m.Task || m.Handoff != nil
	}
	found := false
	for _, m := range messages {
		switch {
		case m.Handoff != nil:
			p := *m.Handoff
			prior = &p
			h.Task = p.Task
			h.Instructions = append([]string(nil), p.Instructions...)
			h.Memory = mergeMemory(h.Memory, p.Memory)
			found = true
		case m.Role == RoleMemory:
			h.Memory = mergeMemory(h.Memory, parseMemory(m.Content))
		case m.Role == RoleUser && !found && (m.Task || !marked):
			h.Task = m.Content
			found = true
			transcript = append(transcript, Message{Role: RoleUser, Content: "[the task, copied above]"})
		case m.Role == RoleUser && found && !loopNotice(m.Content) && strings.TrimSpace(m.Content) != "":
			h.Instructions = append(h.Instructions, m.Content)
			transcript = append(transcript, Message{Role: RoleUser, Content: fmt.Sprintf("[instruction %d, copied above]", len(h.Instructions))})
		default:
			transcript = append(transcript, m)
		}
	}
	return h, prior, transcript
}

// loopNotice reports a user message the loop wrote itself, which is not an
// instruction from the person or the orchestrator.
func loopNotice(content string) bool {
	for _, format := range []string{leakedCallNotice, unsourcedNotice, closingNotice} {
		if prefix, _, _ := strings.Cut(format, "%"); strings.HasPrefix(content, prefix) {
			return true
		}
	}
	return false
}

func (c *SummaryCompactor) summarize(ctx context.Context, system string, h Handoff, prior *Handoff, transcript []Message) (string, *MemoryExtraction, Usage, error) {
	var b strings.Builder
	if s := strings.TrimSpace(system); s != "" {
		if len(s) > maxSummarySystem {
			s = s[:maxSummarySystem] + " ...[cut]"
		}
		fmt.Fprintf(&b, "The agent worked under this system prompt (truncated):\n\n%s\n\n", s)
	}
	b.WriteString("These are copied into the handoff verbatim. They are shown for context only; do not restate them.\n\n")
	fmt.Fprintf(&b, "TASK:\n%s\n\n", h.Task)
	for i, text := range h.Instructions {
		fmt.Fprintf(&b, "INSTRUCTION %d:\n%s\n\n", i+1, text)
	}
	if h.Repo != nil {
		fmt.Fprintf(&b, "REPOSITORY STATE NOW:\n%s\n", h.Repo.render())
	}
	if prior != nil {
		written := *prior
		written.Task, written.Instructions, written.Repo, written.Memory = "", nil, nil, MemoryExtraction{}
		fmt.Fprintf(&b, "PREVIOUS HANDOFF (your sections supersede it; carry forward what still holds):\n\n%s\n", written.modelSections())
	}
	b.WriteString("TRANSCRIPT SINCE:\n\n")
	calls := map[string]ToolCall{}
	for _, m := range transcript {
		switch m.Role {
		case RoleUser:
			fmt.Fprintf(&b, "USER: %s\n\n", m.Content)
		case RoleAssistant:
			if m.Content != "" {
				fmt.Fprintf(&b, "ASSISTANT: %s\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				calls[tc.ID] = tc
				fmt.Fprintf(&b, "CALLED %s(%s)\n", tc.Name, truncateForSummary(string(tc.Arguments), maxSummaryOther))
			}
			b.WriteByte('\n')
		case RoleTool:
			limit := maxSummaryOther
			if failedOrVerified(calls[m.ToolCallID], m.Content) {
				limit = maxSummaryFailed
			}
			fmt.Fprintf(&b, "RESULT: %s\n\n", truncateForSummary(m.Content, limit))
		}
	}

	req := CompletionRequest{
		System:   HandoffInstruction,
		Messages: []Message{{Role: RoleUser, Content: b.String()}},
		Tools:    []Tool{memoryExtractionTool{}},
	}
	resp, err := c.Client.Complete(ctx, req)
	if err != nil {
		return "", nil, Usage{}, fmt.Errorf("summarise transcript: %w", err)
	}
	mem := extractMemory(resp.ToolCalls)
	usage := resp.Usage
	if strings.TrimSpace(resp.Text) == "" {
		// Offered record_memory, a model may answer with the call alone and
		// stop on tool use; ask again with no tool so it can only write.
		retry, err := c.Client.Complete(ctx, CompletionRequest{System: HandoffInstruction, Messages: req.Messages})
		if err != nil {
			return "", nil, usage, fmt.Errorf("summarise transcript: %w", err)
		}
		usage.Add(retry.Usage)
		resp = retry
	}
	if strings.TrimSpace(resp.Text) == "" {
		return "", nil, usage, fmt.Errorf("summarise transcript: model returned nothing")
	}
	return resp.Text, mem, usage, nil
}

// failedOrVerified reports a tool result worth more of the summariser's
// attention: an error, or the output of a build, test or check.
func failedOrVerified(call ToolCall, content string) bool {
	lower := strings.ToLower(content)
	for _, marker := range []string{"error", "fail", "panic:"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	args := strings.ToLower(string(call.Arguments))
	for _, marker := range []string{"test", "build", "vet", "lint", "check", " ci"} {
		if strings.Contains(args, marker) {
			return true
		}
	}
	return false
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

func truncateForSummary(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut, _ := TruncateMiddle(s, limit)
	return cut
}
