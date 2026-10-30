package agent

import (
	"fmt"
	"strings"
)

// tailShare is the fraction of the budget reserved for the end of the output.
// Build and test failures put their verdict last, so the tail is worth more
// than the head when both cannot fit.
const tailShare = 0.6

// TruncateMiddle shrinks s to fit maxBytes by dropping its middle, keeping
// the head and a larger tail, and stating how much was removed.
//
// It reports whether anything was dropped. The cut is aligned to line
// boundaries so neither retained part starts or ends mid-line.
func TruncateMiddle(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, false
	}

	const noticeReserve = 80
	budget := maxBytes - noticeReserve
	if budget < 0 {
		budget = 0
	}
	tailBudget := int(float64(budget) * tailShare)
	headBudget := budget - tailBudget

	head := alignToLineEnd(s[:headBudget])
	tail := alignToLineStart(s[len(s)-tailBudget:])
	dropped := len(s) - len(head) - len(tail)

	var b strings.Builder
	b.Grow(len(head) + len(tail) + noticeReserve)
	b.WriteString(head)
	fmt.Fprintf(&b, "\n... [%d bytes truncated] ...\n", dropped)
	b.WriteString(tail)
	return b.String(), true
}

// alignToLineEnd trims a trailing partial line.
func alignToLineEnd(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// alignToLineStart trims a leading partial line.
func alignToLineStart(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
