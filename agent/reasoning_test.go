package agent

import "testing"

func TestDeepSeekThinksUnlessAskedNotTo(t *testing.T) {
	for _, model := range []string{"deepseek-r1", "DeepSeek-V3", "deepseek-v4-flash-0731"} {
		if got := DefaultReasoningEffort(model); got != "" {
			t.Errorf("DefaultReasoningEffort(%q) = %q, want the server default", model, got)
		}
	}
}

func TestModelsThatDoNotReasonKeepTheServerDefault(t *testing.T) {
	for _, model := range []string{"qwen3-coder-next", "devstral-small", ""} {
		if got := DefaultReasoningEffort(model); got != "" {
			t.Errorf("DefaultReasoningEffort(%q) = %q, want the server default", model, got)
		}
	}
}

func TestEffortIsCheckedBeforeItIsSent(t *testing.T) {
	c := &httpClient{}
	if c.SetReasoningEffort("medium") != true || c.ReasoningEffort() != "medium" {
		t.Fatal("a valid effort was refused")
	}
	if c.SetReasoningEffort("very hard") {
		t.Fatal("an effort no server accepts was let through")
	}
	if c.ReasoningEffort() != "medium" {
		t.Fatal("a refused value overwrote the one in force")
	}
}

func TestAThinkingModelIsAskedNotTo(t *testing.T) {
	// Left to think, it spends the turn's budget on that and a short question
	// comes back with nothing said — the answer never reaches content.
	if got := DefaultReasoningEffort("qwen3.5-397b-a17b"); got != ReasoningEffortNone {
		t.Errorf("DefaultReasoningEffort(qwen3.5) = %q, want %q", got, ReasoningEffortNone)
	}
	// Its coder sibling does not reason, so there is nothing to turn off.
	if got := DefaultReasoningEffort("qwen3-coder-30b-a3b-instruct"); got != "" {
		t.Errorf("DefaultReasoningEffort(qwen3-coder) = %q, want the server default", got)
	}
}
