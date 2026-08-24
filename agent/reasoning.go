package agent

import "strings"

// ReasoningEfforts are the values a server accepts, from most thinking to
// none. An empty string is distinct from all of them: it leaves the server's
// own default alone.
var ReasoningEfforts = []string{"high", "medium", "low", ReasoningEffortNone}

// ReasoningEffortNone turns reasoning off. It is not sent as a value: some
// servers reject it, and the absence of the field says the same thing.
const ReasoningEffortNone = "none"

// ValidReasoningEffort reports whether v is one the servers understand.
func ValidReasoningEffort(v string) bool {
	if v == "" {
		return true
	}
	for _, e := range ReasoningEfforts {
		if v == e {
			return true
		}
	}
	return false
}

// DefaultReasoningEffort is what a model should be asked for when the
// operator has not said.
//
// DeepSeek reasons by default and returns its chain of thought in a separate
// reasoning_content field that this client never reads, so the thinking is
// billed as output tokens and then discarded. Measured over the same three
// tasks, turning it off scored better on every axis. Models that do not
// reason ignore the field, so naming them changes nothing but is what makes
// the table say who was measured and who was assumed.
func DefaultReasoningEffort(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "deepseek"):
		return ReasoningEffortNone
	// A coder model in the same family does not reason, and asking it to stop
	// would be asking about something it does not do.
	case strings.Contains(m, "qwen") && !strings.Contains(m, "coder"):
		// It thinks by default and spends the turn's budget doing it, which
		// on a short question leaves nothing said at all. An agent that runs
		// tools reasons through them instead.
		return ReasoningEffortNone
	default:
		return ""
	}
}

// SetReasoningEffort changes how much the model thinks, for the calls that
// follow. It reports false for a value no server would accept.
func (c *httpClient) SetReasoningEffort(v string) bool {
	if !ValidReasoningEffort(v) {
		return false
	}
	c.provider.ReasoningEffort = v
	return true
}

// ReasoningEffort is what the next call will ask for.
func (c *httpClient) ReasoningEffort() string { return c.provider.ReasoningEffort }
