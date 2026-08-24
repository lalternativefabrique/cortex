package agent

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestDeepSeekDefaultsToNoThinking(t *testing.T) {
	// Measured, not assumed: the chain of thought comes back in a field this
	// client does not read, so it is paid for and dropped.
	for _, model := range []string{"deepseek-r1", "DeepSeek-V3", "scw/deepseek-r1-distill"} {
		if got := DefaultReasoningEffort(model); got != "none" {
			t.Errorf("DefaultReasoningEffort(%q) = %q, want none", model, got)
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

func TestNoneIsNeverSentOnTheWire(t *testing.T) {
	// vLLM validates reasoning_effort against low, medium and high, and
	// rejects the request outright on anything else — so "none" reached the
	// server as a 400 rather than as less thinking. Asking for no reasoning
	// is expressed by not asking for any.
	if got := wireReasoningEffort(ReasoningEffortNone); got != "" {
		t.Fatalf("wireReasoningEffort(none) = %q, want the field omitted", got)
	}
	for _, effort := range []string{"low", "medium", "high", ""} {
		if got := wireReasoningEffort(effort); got != effort {
			t.Errorf("wireReasoningEffort(%q) = %q, want it unchanged", effort, got)
		}
	}
}

func TestTheRequestOmitsNoneEntirely(t *testing.T) {
	// The JSON is what the server sees; a test on the field alone would pass
	// while omitempty still let the value through.
	c := &httpClient{provider: Provider{Model: "deepseek-v4-flash-0731", ReasoningEffort: ReasoningEffortNone}}
	req, err := c.buildPayload(CompletionRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("reasoning_effort")) {
		t.Fatalf("the field is on the wire: %s", body)
	}
}
