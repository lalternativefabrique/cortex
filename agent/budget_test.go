package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMaybeCompactUsesCompactBudgetWhenSet(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "the summary"}}}
	r, err := NewRunner(Config{
		Client:        client,
		ContextWindow: 100,
		Compactor:     &stubCompactor{},
		CompactBudget: func(BudgetState) float64 { return 0.01 },
	})
	if err != nil {
		t.Fatal(err)
	}

	_, did, _, err := r.maybeCompact(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("maybeCompact did not compact under a budget that lowers the threshold to near zero")
	}
}

func TestMaybeCompactFallsBackToCompactAtWithNoCompactBudget(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "unused"}}}
	r, err := NewRunner(Config{
		Client:        client,
		ContextWindow: 1_000_000,
		Compactor:     &stubCompactor{},
		CompactBudget: NoCompactBudget,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, did, _, err := r.maybeCompact(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Fatal("maybeCompact compacted a tiny history against a huge window with the fixed 0.75 fraction")
	}
}

func TestNewRunnerInstallsDefaultCompactBudgetWhenUnset(t *testing.T) {
	client := &scriptedClient{}
	r, err := NewRunner(Config{Client: client})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.CompactBudget == nil {
		t.Fatal("NewRunner left CompactBudget nil; want DefaultCompactBudget installed")
	}
}

func TestDefaultCompactBudgetLowersThresholdNearStepLimit(t *testing.T) {
	got := DefaultCompactBudget(BudgetState{Steps: 55, MaxSteps: 60})
	if got >= DefaultCompactAt {
		t.Fatalf("DefaultCompactBudget near the step limit = %v, want less than %v", got, DefaultCompactAt)
	}
}

func TestDefaultCompactBudgetIsUnchangedFarFromTheStepLimit(t *testing.T) {
	got := DefaultCompactBudget(BudgetState{Steps: 1, MaxSteps: 60})
	if got != DefaultCompactAt {
		t.Fatalf("DefaultCompactBudget far from the step limit = %v, want %v", got, DefaultCompactAt)
	}
}

func TestDefaultCompactBudgetLowersThresholdOnRepeatedToolErrors(t *testing.T) {
	quiet := DefaultCompactBudget(BudgetState{MaxSteps: 60, ToolErrors: 0})
	noisy := DefaultCompactBudget(BudgetState{MaxSteps: 60, ToolErrors: 3})
	if noisy >= quiet {
		t.Fatalf("DefaultCompactBudget with 3 tool errors = %v, want less than %v", noisy, quiet)
	}
}

func TestDefaultCompactBudgetLowersThresholdOnRepeatedToolCalls(t *testing.T) {
	fresh := DefaultCompactBudget(BudgetState{MaxSteps: 60, Repeats: 0})
	stuck := DefaultCompactBudget(BudgetState{MaxSteps: 60, Repeats: 2})
	if stuck >= fresh {
		t.Fatalf("DefaultCompactBudget with 2 repeated tool calls = %v, want less than %v", stuck, fresh)
	}
}

func TestRepeatedToolCallsInCountsMatchingNameAndArguments(t *testing.T) {
	history := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "bash", Arguments: json.RawMessage(`{"cmd":"pwd"}`)}}},
	}
	if got := repeatedToolCallsIn(history, len(history)); got != 1 {
		t.Fatalf("repeatedToolCallsIn = %d, want 1 (only the second \"ls\" repeats)", got)
	}
	if got := repeatedToolCallsIn(history, 1); got != 0 {
		t.Fatalf("repeatedToolCallsIn(keepRecent=1) = %d, want 0 (window too short to see the repeat)", got)
	}
}

func TestToolErrorsInCountsOnlyWithinKeepRecent(t *testing.T) {
	history := []Message{
		{Role: RoleTool, Content: "error: old failure"},
		{Role: RoleUser, Content: "padding"},
		{Role: RoleTool, Content: "error: recent failure"},
		{Role: RoleAssistant, Content: "ok"},
	}
	if got := toolErrorsIn(history, 2); got != 1 {
		t.Fatalf("toolErrorsIn(keepRecent=2) = %d, want 1 (the old failure is out of range)", got)
	}
	if got := toolErrorsIn(history, len(history)); got != 2 {
		t.Fatalf("toolErrorsIn(keepRecent=len) = %d, want 2", got)
	}
}
