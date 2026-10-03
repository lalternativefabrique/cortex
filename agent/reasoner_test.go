package agent

import (
	"encoding/json"
	"testing"
)

func wireFor(t *testing.T, model, effort string) map[string]any {
	t.Helper()
	c := &httpClient{provider: Provider{Model: model, ReasoningEffort: effort}}
	req, err := c.buildPayload(CompletionRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func templateFlag(w map[string]any, key string) (any, bool) {
	kw, _ := w["chat_template_kwargs"].(map[string]any)
	v, ok := kw[key]
	return v, ok
}

func TestDeepSeekIsToldNotToThinkBothWays(t *testing.T) {
	// Scaleway reasons by default and stops only on reasoning_effort "none":
	// leaving the field out had DeepSeek think on every turn.
	w := wireFor(t, "deepseek-v4-flash-0731", ReasoningEffortNone)
	if w["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none", w["reasoning_effort"])
	}
	if v, _ := templateFlag(w, "thinking"); v != false {
		t.Errorf("thinking = %v, want false", v)
	}
	if v, _ := templateFlag(wireFor(t, "deepseek-v4-flash-0731", "high"), "thinking"); v != true {
		t.Errorf("thinking = %v on high, want true", v)
	}
}

func TestQwenStopsThinkingWithoutTheEffortField(t *testing.T) {
	// vLLM rejects reasoning_effort "none" with a 400.
	w := wireFor(t, "Qwen3.5-397B-A17B", ReasoningEffortNone)
	if _, ok := w["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort sent: %v", w["reasoning_effort"])
	}
	if v, _ := templateFlag(w, "enable_thinking"); v != false {
		t.Errorf("enable_thinking = %v, want false", v)
	}
}

func TestGemmaTakesOnlyItsTemplateFlag(t *testing.T) {
	w := wireFor(t, "gemma-3-27b-it", ReasoningEffortNone)
	if _, ok := w["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort sent: %v", w["reasoning_effort"])
	}
	if v, _ := templateFlag(w, "enable_thinking"); v != false {
		t.Errorf("enable_thinking = %v, want false", v)
	}
}

func TestGptOSSCannotGoBelowLow(t *testing.T) {
	if w := wireFor(t, "gpt-oss-20b", ReasoningEffortNone); w["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want low", w["reasoning_effort"])
	}
}

func TestMistralSendsAnEffortOnlyToMagistral(t *testing.T) {
	if w := wireFor(t, "mistral-small-3.2-24b-instruct-2506", "high"); w["reasoning_effort"] != nil {
		t.Errorf("a model that does not reason was sent %v", w["reasoning_effort"])
	}
	if w := wireFor(t, "magistral-medium", ReasoningEffortNone); w["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want none", w["reasoning_effort"])
	}
}

func TestAnUnknownModelNeverSeesNone(t *testing.T) {
	w := wireFor(t, "some-model", ReasoningEffortNone)
	if _, ok := w["reasoning_effort"]; ok {
		t.Errorf("reasoning_effort sent: %v", w["reasoning_effort"])
	}
	if _, ok := w["chat_template_kwargs"]; ok {
		t.Errorf("chat_template_kwargs sent: %v", w["chat_template_kwargs"])
	}
	if w := wireFor(t, "some-model", "medium"); w["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", w["reasoning_effort"])
	}
}

func TestNoEffortSendsNothing(t *testing.T) {
	for _, m := range []string{"deepseek-v4", "qwen3", "gemma-3", "magistral", "mistral-small"} {
		w := wireFor(t, m, "")
		if _, ok := w["reasoning_effort"]; ok {
			t.Errorf("%s: reasoning_effort sent", m)
		}
		if _, ok := w["chat_template_kwargs"]; ok {
			t.Errorf("%s: chat_template_kwargs sent", m)
		}
	}
}
