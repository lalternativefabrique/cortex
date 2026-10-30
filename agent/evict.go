package agent

import (
	"encoding/json"
	"fmt"
)

// evictionNotice replaces the body of a superseded tool result.
const evictionNotice = "[superseded — this result was replaced by a later call to the same target; only the newer result is current]"

// EvictStale replaces tool results the conversation has since superseded.
//
// When a model reads a file, edits it and reads it again, the first read is
// no longer true: it describes content that has changed. Keeping it costs
// tokens for every remaining step and, worse, leaves two contradictory
// versions in context for the model to choose between.
//
// Only results that a later call demonstrably replaced are evicted, keyed by
// the tool and its target. The most recent result for each key survives, as
// does everything within keepRecent messages of the end — recent results are
// what the model is actively working from, and evicting them mid-task is how
// an agent loses its thread.
//
// The messages themselves stay in place: dropping a tool message would
// orphan the assistant turn that requested it, which providers reject.
func EvictStale(messages []Message, keepRecent int) ([]Message, int) {
	if keepRecent < 0 {
		keepRecent = 0
	}
	cutoff := len(messages) - keepRecent

	// Walk backwards so the first occurrence of each key is the newest.
	seen := map[string]struct{}{}
	stale := map[int]struct{}{}
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != RoleTool || m.Content == evictionNotice {
			continue
		}
		key := supersedeKey(messages, i)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			continue
		}
		if i < cutoff {
			stale[i] = struct{}{}
		}
	}
	if len(stale) == 0 {
		return messages, 0
	}

	out := make([]Message, len(messages))
	copy(out, messages)
	freed := 0
	for i := range stale {
		freed += EstimateTokens(out[i].Content) - EstimateTokens(evictionNotice)
		out[i].Content = evictionNotice
	}
	if freed < 0 {
		freed = 0
	}
	return out, freed
}

// supersedeKey identifies what a tool result describes, so a later result
// about the same thing can be recognised as replacing it.
//
// Only reads and searches are keyed: their results are snapshots that go out
// of date. A bash result is not — two runs of the same command at different
// points in a session are genuinely different observations, and the earlier
// one may be the evidence the model is reasoning from.
func supersedeKey(messages []Message, toolIndex int) string {
	call, ok := callFor(messages, toolIndex)
	if !ok {
		return ""
	}
	var args struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return ""
	}

	switch call.Name {
	case "read":
		// A paged read is not superseded by a read of a different page.
		return fmt.Sprintf("read:%s:%d:%d", args.Path, args.Offset, args.Limit)
	case "grep", "glob":
		return call.Name + ":" + args.Pattern + ":" + args.Path
	default:
		return ""
	}
}

// callFor finds the tool call a tool message answers.
func callFor(messages []Message, toolIndex int) (ToolCall, bool) {
	id := messages[toolIndex].ToolCallID
	if id == "" {
		return ToolCall{}, false
	}
	for i := toolIndex - 1; i >= 0; i-- {
		if messages[i].Role != RoleAssistant {
			continue
		}
		for _, tc := range messages[i].ToolCalls {
			if tc.ID == id {
				return tc, true
			}
		}
	}
	return ToolCall{}, false
}
