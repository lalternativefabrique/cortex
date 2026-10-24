package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// FigureCheck reports whether the figures of an answer can be found in what
// the run read. Checked is zero when the run read no source, which is not
// the same as an answer whose every figure was found.
type FigureCheck struct {
	Checked   int
	Sourced   int
	Unsourced []string
}

// figurePattern matches a number as written in prose: thousands grouped by
// a space, a narrow or no-break space, a dot or a comma, and a decimal part.
// A digit glued to a letter (W5, H2O, mp3) is a name, not a figure.
var figurePattern = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(\d{1,3}(?:[  \x{202F}.,]\d{3})+(?:[.,]\d+)?|\d+(?:[.,]\d+)?)`)

// figureDigits keeps a figure's digits only, so "1 477,93", "1477.93" and
// "1 477.93" are one figure. A lone digit says too little to check: it is a
// list marker or a count as often as a quantity.
func figureDigits(raw string) (string, bool) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, raw)
	return digits, len(digits) >= 2
}

func answerFigures(text string) []string {
	var out []string
	for _, m := range figurePattern.FindAllStringSubmatch(text, -1) {
		raw := strings.TrimRight(m[1], ".,")
		if _, ok := figureDigits(raw); ok && !slices.Contains(out, raw) {
			out = append(out, raw)
		}
	}
	return out
}

// sourceDigits rewrites a source so a figure is a single run of digits
// delimited by anything else, whatever separators the page used.
func sourceDigits(source string) string {
	var b strings.Builder
	for _, m := range figurePattern.FindAllStringSubmatch(source, -1) {
		digits, _ := figureDigits(m[1])
		b.WriteString(" ")
		b.WriteString(digits)
	}
	b.WriteString(" ")
	return b.String()
}

func checkFigures(answer string, sources []string) FigureCheck {
	figures := answerFigures(answer)
	if len(figures) == 0 || len(sources) == 0 {
		return FigureCheck{}
	}
	corpus := sourceDigits(strings.Join(sources, "\n"))
	check := FigureCheck{Checked: len(figures)}
	for _, f := range figures {
		digits, _ := figureDigits(f)
		if strings.Contains(corpus, " "+digits+" ") {
			check.Sourced++
			continue
		}
		check.Unsourced = append(check.Unsourced, f)
	}
	return check
}

const unsourcedNotice = "These figures in your answer appear in none of the sources you read this turn: %s. Correct each one from the sources, or remove it, or say plainly it is not from them. Give the whole answer again."

func unsourcedMessage(figures []string) string {
	return fmt.Sprintf(unsourcedNotice, strings.Join(figures, ", "))
}
