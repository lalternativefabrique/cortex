package agent

import "strings"

// ReasoningEfforts are the values a server accepts, from most thinking to
// none. An empty string is distinct from all of them: it leaves the server's
// own default alone.
var ReasoningEfforts = []string{"high", "medium", "low", ReasoningEffortNone}

// ReasoningEffortNone turns reasoning off. How it reaches the server depends on
// the model family: see reasonerFor.
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
// operator has not said. Empty leaves the server's default alone.
func DefaultReasoningEffort(model string) string {
	m := strings.ToLower(model)
	// Qwen thinks by default and spends the turn's budget doing it, which on
	// a short question leaves nothing said at all. Its coder sibling does not
	// reason, so there is nothing to turn off.
	if strings.Contains(m, "qwen") && !strings.Contains(m, "coder") {
		return ReasoningEffortNone
	}
	return ""
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
