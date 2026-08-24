package agent

import "unicode"

// EstimateTokens approximates how many tokens a string occupies.
//
// It is deliberately not a tokenizer. Loading the model's own vocabulary
// would tie the harness to one model family, and the estimate is only ever
// used to decide when to compact — a decision that tolerates being wrong by
// a few percent as long as it errs high. Code sits around 3 characters per
// token: denser than prose because identifiers and punctuation fragment,
// and the divisor below leans low so the estimate over-reports rather than
// letting a run walk into a context-length error.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	var letters, other int
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
			continue
		}
		other++
	}
	// Punctuation and whitespace tokenize far less efficiently than words,
	// so they are counted closer to one token apiece.
	return letters/4 + other/2 + 1
}

// EstimateMessages approximates the token cost of a conversation, including
// the per-message framing every provider adds.
func EstimateMessages(system string, messages []Message) int {
	const perMessageOverhead = 4

	total := EstimateTokens(system)
	for _, m := range messages {
		total += EstimateTokens(m.Content) + perMessageOverhead
		for _, tc := range m.ToolCalls {
			total += EstimateTokens(tc.Name) + EstimateTokens(string(tc.Arguments))
		}
	}
	return total
}

// EstimateTools approximates what the tool definitions cost. They sit in the
// stable prefix and are resent on every step, so they are part of the budget
// even though they never change.
func EstimateTools(tools []Tool) int {
	total := 0
	for _, t := range tools {
		total += EstimateTokens(t.Name()) + EstimateTokens(t.Description())
		if raw, err := SchemaFor(t.InputSchema()); err == nil {
			total += EstimateTokens(string(raw))
		}
	}
	return total
}
