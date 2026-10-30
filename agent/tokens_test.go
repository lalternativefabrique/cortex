package agent

import (
	"strings"
	"testing"
)

func TestEstimateTokensGrowsWithLength(t *testing.T) {
	short := EstimateTokens("func main() {}")
	long := EstimateTokens(strings.Repeat("func main() {}\n", 100))
	if long <= short {
		t.Fatalf("estimate did not grow with input: %d vs %d", short, long)
	}
}

func TestEstimateTokensErrsHighOnCode(t *testing.T) {
	// Roughly 3 characters per token is the working figure for code; the
	// estimate must not land below it, or a run walks into a context-length
	// error believing it had room.
	code := strings.Repeat("if err != nil { return fmt.Errorf(\"x: %w\", err) }\n", 50)
	got := EstimateTokens(code)
	floor := len(code) / 4
	if got < floor {
		t.Fatalf("estimate %d is below the %d floor for %d bytes of code", got, floor, len(code))
	}
}

func TestEstimateTokensHandlesEmpty(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens(\"\") = %d, want 0", got)
	}
}

func TestEstimateMessagesCountsToolCalls(t *testing.T) {
	plain := []Message{{Role: RoleAssistant, Content: "hello"}}
	withCall := []Message{{
		Role:      RoleAssistant,
		Content:   "hello",
		ToolCalls: []ToolCall{{Name: "bash", Arguments: []byte(`{"command":"go test ./..."}`)}},
	}}
	if EstimateMessages("", withCall) <= EstimateMessages("", plain) {
		t.Fatal("tool call arguments are not counted toward the budget")
	}
}

func TestEstimateMessagesCountsSystemPrompt(t *testing.T) {
	msgs := []Message{{Role: RoleUser, Content: "hi"}}
	bare := EstimateMessages("", msgs)
	withSystem := EstimateMessages(strings.Repeat("instructions ", 200), msgs)
	if withSystem <= bare {
		t.Fatal("the system prompt is not counted, yet it is resent every step")
	}
}

func TestEstimateToolsCountsDescriptions(t *testing.T) {
	verbose := &fakeTool{name: "verbose"}
	got := EstimateTools([]Tool{verbose})
	if got <= 0 {
		t.Fatalf("EstimateTools = %d, want a positive cost", got)
	}
}
