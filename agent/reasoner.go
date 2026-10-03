package agent

import "strings"

// reasoner puts a reasoning effort on the wire the way a model family
// understands it. Families disagree on the switch: some take reasoning_effort,
// some only a chat template flag, some cannot stop thinking at all.
type reasoner interface {
	Apply(effort string, req *wireRequest)
}

// reasonerFor picks the family adapter from the model name.
func reasonerFor(model string) reasoner {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "deepseek"):
		return deepseekReasoner{}
	case strings.Contains(m, "qwen"):
		return qwenReasoner{}
	case strings.Contains(m, "gemma"):
		return gemmaReasoner{}
	case strings.Contains(m, "gpt-oss"):
		return gptOSSReasoner{}
	case strings.Contains(m, "magistral"):
		return effortReasoner{}
	case strings.Contains(m, "mistral"), strings.Contains(m, "devstral"), strings.Contains(m, "codestral"):
		return silentReasoner{}
	default:
		return omitNoneReasoner{}
	}
}

// deepseekReasoner: Scaleway turns DeepSeek's thinking off on
// reasoning_effort "none" and vLLM on the template's thinking flag; with
// neither it thinks, and leaving the field out reads as "think".
type deepseekReasoner struct{}

func (deepseekReasoner) Apply(effort string, req *wireRequest) {
	if effort == "" {
		return
	}
	req.ReasoningEffort = effort
	req.templateFlag("thinking", effort != ReasoningEffortNone)
}

// qwenReasoner: vLLM rejects reasoning_effort "none", so thinking is turned
// off through the template's enable_thinking flag alone.
type qwenReasoner struct{}

func (qwenReasoner) Apply(effort string, req *wireRequest) {
	switch effort {
	case "":
	case ReasoningEffortNone:
		req.templateFlag("enable_thinking", false)
	default:
		req.ReasoningEffort = effort
		req.templateFlag("enable_thinking", true)
	}
}

// gemmaReasoner: Gemma's template takes no effort, only enable_thinking.
type gemmaReasoner struct{}

func (gemmaReasoner) Apply(effort string, req *wireRequest) {
	if effort == "" {
		return
	}
	req.templateFlag("enable_thinking", effort != ReasoningEffortNone)
}

// gptOSSReasoner: gpt-oss cannot stop reasoning; the least it does is low.
type gptOSSReasoner struct{}

func (gptOSSReasoner) Apply(effort string, req *wireRequest) {
	if effort == ReasoningEffortNone {
		effort = "low"
	}
	req.ReasoningEffort = effort
}

// effortReasoner sends the effort as asked, "none" included.
type effortReasoner struct{}

func (effortReasoner) Apply(effort string, req *wireRequest) { req.ReasoningEffort = effort }

// silentReasoner is for models that do not reason: the field is never sent.
type silentReasoner struct{}

func (silentReasoner) Apply(string, *wireRequest) {}

// omitNoneReasoner is for an unknown model behind an unknown server: "none" is
// left out, since vLLM rejects it, and any other effort is sent.
type omitNoneReasoner struct{}

func (omitNoneReasoner) Apply(effort string, req *wireRequest) {
	if effort != ReasoningEffortNone {
		req.ReasoningEffort = effort
	}
}

func (r *wireRequest) templateFlag(key string, on bool) {
	if r.ChatTemplateKwargs == nil {
		r.ChatTemplateKwargs = map[string]any{}
	}
	r.ChatTemplateKwargs[key] = on
}
