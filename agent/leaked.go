package agent

import (
	"regexp"
	"strings"
)

// leakedCallMarkers are the shapes a model emits when it falls back to the
// tool syntax it was trained on instead of the protocol's structured form.
//
// Observed in practice from two different families: Mistral's Devstral emits
// [TOOL_CALLS][...], Qwen emits <function=name>. A server that does not
// recognise the model's dialect passes it through as ordinary text, and the
// turn then looks final when the model was mid-thought.
var leakedCallMarkers = []*regexp.Regexp{
	regexp.MustCompile(`\[TOOL_CALLS\]`),
	regexp.MustCompile(`<function=[a-zA-Z_][\w-]*>`),
	regexp.MustCompile(`<tool_call>`),
	regexp.MustCompile(`<\|tool_call\|>`),
	regexp.MustCompile(`(?m)^\s*<invoke name="`),
}

// LeakedToolCall reports whether text carries a tool call the server failed
// to parse.
//
// A false positive costs one wasted step; a false negative ends a run that
// was still working. The markers are therefore specific enough that ordinary
// prose about tools does not match — a sentence mentioning "tool_call" in
// passing has no angle brackets around it.
func LeakedToolCall(text string) bool {
	if text == "" {
		return false
	}
	for _, re := range leakedCallMarkers {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// leakedCallNotice is fed back so the model retries in the protocol's form.
//
// It names what was seen rather than scolding: a model that fell back to its
// training dialect did so because something in the exchange knocked it off
// the protocol, and telling it plainly is what gets the next turn right.
const leakedCallNotice = `Your last reply contained a tool call written as text rather than as a structured tool call, so nothing was executed. Emit it through the tool-calling interface instead — do not write the call out in the message body.`

// firstLeakedMarker names which dialect leaked, for diagnostics.
func firstLeakedMarker(text string) string {
	for _, re := range leakedCallMarkers {
		if m := re.FindString(text); m != "" {
			return strings.TrimSpace(m)
		}
	}
	return ""
}
