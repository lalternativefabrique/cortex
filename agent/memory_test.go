package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryExtractionRenderOmitsEmptySections(t *testing.T) {
	got := MemoryExtraction{Decisions: []string{"picked PostgreSQL"}}.Render()
	if !strings.Contains(got, "Decisions:") {
		t.Fatalf("render() = %q, missing Decisions section", got)
	}
	if strings.Contains(got, "Constraints:") || strings.Contains(got, "Facts:") {
		t.Fatalf("render() = %q, an empty section should be omitted entirely", got)
	}
}

func TestSummaryCompactorExtractsMemoryWhenModelCallsTool(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{
		Text: "the summary",
		ToolCalls: []ToolCall{{
			Name:      "record_memory",
			Arguments: json.RawMessage(`{"facts":["main.go has func main"]}`),
		}},
	}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, KeepFirst: 1}

	out, mem, _, err := c.Compact(context.Background(), "sys", longHistory(10))
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, m := range out {
		if m.Role == RoleMemory && strings.Contains(m.Content, "main.go has func main") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no RoleMemory message with the extracted fact in output: %+v", out)
	}
	if mem == nil || len(mem.Facts) != 1 || mem.Facts[0] != "main.go has func main" {
		t.Fatalf("Compact() returned mem = %+v, want the extracted fact", mem)
	}
}

func TestSummaryCompactorFallsBackToProseWhenToolNotCalled(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "the summary"}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, KeepFirst: 1}

	out, mem, _, err := c.Compact(context.Background(), "sys", longHistory(10))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out {
		if m.Role == RoleMemory {
			t.Fatalf("a RoleMemory message appeared though the model never called record_memory: %+v", out)
		}
	}
	if mem != nil {
		t.Fatalf("Compact() returned mem = %+v, want nil when the model never called record_memory", mem)
	}
}

func TestSummaryCompactorFallsBackWhenToolArgumentsAreMalformed(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{
		Text:      "the summary",
		ToolCalls: []ToolCall{{Name: "record_memory", Arguments: json.RawMessage(`not json`)}},
	}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, KeepFirst: 1}

	out, mem, _, err := c.Compact(context.Background(), "sys", longHistory(10))
	if err != nil {
		t.Fatalf("Compact() with malformed tool arguments should degrade to prose-only, got error: %v", err)
	}
	for _, m := range out {
		if m.Role == RoleMemory {
			t.Fatalf("a RoleMemory message appeared despite malformed arguments: %+v", out)
		}
	}
	if mem != nil {
		t.Fatalf("Compact() returned mem = %+v, want nil when the arguments were malformed", mem)
	}
}

func TestSummaryCompactorPreservesPinnedMemoryAcrossRepeatedCompactions(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "second summary"}}}
	c := &SummaryCompactor{Client: client, KeepRecent: 4, KeepFirst: 1}

	pinned := Message{Role: RoleMemory, Content: "Decisions:\n- picked PostgreSQL\n"}
	history := longHistory(10)
	// Bury the pinned memory inside what will become `body` on this call.
	history = append(history[:2], append([]Message{pinned}, history[2:]...)...)

	out, _, _, err := c.Compact(context.Background(), "sys", history)
	if err != nil {
		t.Fatal(err)
	}

	var stillPresent bool
	for _, m := range out {
		if m.Role == RoleMemory && m.Content == pinned.Content {
			stillPresent = true
		}
	}
	if !stillPresent {
		t.Fatalf("pinned memory did not survive a second compaction: %+v", out)
	}

	sent := client.calls[0].Messages[0].Content
	if strings.Contains(sent, pinned.Content) {
		t.Fatal("the pinned memory's content was sent to the model for re-summarisation")
	}
}
