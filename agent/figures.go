package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// FigureCheck reports whether the figures of an answer can be found in what
// the run read. Checked is zero when the run read no source, which is not
// the same as an answer whose every figure was found.
type FigureCheck struct {
	Checked   int              `json:"checked"`
	Sourced   int              `json:"sourced"`
	Unsourced []string         `json:"unsourced"`
	Evidence  []FigureEvidence `json:"evidence"`
}

// FigureEvidence is where a figure of the answer stands: the passage of the
// page that carries it, or the calculation the answer showed over figures
// that are themselves found. Ref, Title and URL are empty for a figure the
// person gave or a tool other than a web page returned.
type FigureEvidence struct {
	Figure  string `json:"figure"`
	Ref     string `json:"ref,omitempty"`
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
	Passage string `json:"passage,omitempty"`
	Formula string `json:"formula,omitempty"`
}

// Citation is a web page the answer rests on: cited by its ref, or carrying
// one of its figures.
type Citation struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// source is one text the run read: a web page when Ref is set, otherwise a
// message from the person or a tool result that is not a page.
type source struct {
	Ref   string
	Title string
	URL   string
	Text  string
}

// sourcesOf splits a tool result into the pages it carries when it is the
// web tools' JSON list of pages, and keeps it whole otherwise.
func sourcesOf(content string) []source {
	var pages []struct {
		Ref     string `json:"ref"`
		Title   string `json:"title"`
		URL     string `json:"url"`
		Excerpt string `json:"excerpt"`
	}
	if json.Unmarshal([]byte(content), &pages) != nil || len(pages) == 0 || pages[0].Ref == "" {
		return []source{{Text: content}}
	}
	out := make([]source, 0, len(pages))
	for _, p := range pages {
		out = append(out, source{Ref: p.Ref, Title: p.Title, URL: p.URL, Text: p.Title + "\n" + p.Excerpt})
	}
	return out
}

// figurePattern matches a time (11h00, 9 h 30, 14:05) or a number as written
// in prose: thousands grouped by a space, a narrow or no-break space, a dot
// or a comma, and a decimal part. A digit glued to a letter before it (W5,
// H2O, mp3) is a name, not a figure.
var figurePattern = regexp.MustCompile(`(?:^|[^\p{L}\p{N}])(\d{1,2} ?[hH:] ?\d{2}|\d{1,3}(?:[  \x{202F}.,]\d{3})+(?:[.,]\d+)?|\d+(?:[.,]\d+)?)`)

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

// passageRunes is how much of the page is kept around a figure: enough for
// the sentence that carries it, short enough to show under an answer.
const passageRunes = 90

// findIn returns the passage of text where a figure with these digits is
// written, whatever separators the text used.
func findIn(text, digits string) (string, bool) {
	for _, loc := range figurePattern.FindAllStringSubmatchIndex(text, -1) {
		if d, _ := figureDigits(text[loc[2]:loc[3]]); d == digits {
			return passageAround(text, loc[2], loc[3]), true
		}
	}
	return "", false
}

func passageAround(text string, start, end int) string {
	before := []rune(text[:start])
	after := []rune(text[end:])
	from := max(0, len(before)-passageRunes)
	to := min(len(after), passageRunes)
	passage := string(before[from:]) + text[start:end] + string(after[:to])
	passage = strings.Join(strings.Fields(passage), " ")
	if from > 0 {
		passage = "…" + passage
	}
	if to < len(after) {
		passage += "…"
	}
	return passage
}

func locate(digits string, sources []source) (FigureEvidence, bool) {
	var fallback *FigureEvidence
	for _, s := range sources {
		passage, ok := findIn(s.Text, digits)
		if !ok {
			continue
		}
		ev := FigureEvidence{Ref: s.Ref, Title: s.Title, URL: s.URL, Passage: passage}
		if s.Ref != "" {
			return ev, true
		}
		if fallback == nil {
			fallback = &ev
		}
	}
	if fallback != nil {
		return *fallback, true
	}
	return FigureEvidence{}, false
}

var operationPattern = regexp.MustCompile(`(\d[\d  \x{202F}.,]*\d|\d)\s*([×x*+−\-/÷])\s*(\d[\d  \x{202F}.,]*\d|\d)`)

var dotThousands = regexp.MustCompile(`^\d{1,3}(\.\d{3})+$`)

// figureValue reads a figure the French way and the English way alike: a
// comma is decimal, and so is a dot unless it groups thousands (1.477.930).
func figureValue(raw string) (float64, int, bool) {
	s := strings.NewReplacer(" ", "", " ", "", " ", "").Replace(strings.TrimSpace(raw))
	if dotThousands.MatchString(s) {
		s = strings.ReplaceAll(s, ".", "")
	}
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	}
	v, err := strconv.ParseFloat(s, 64)
	decimals := 0
	if i := strings.IndexByte(s, '.'); i >= 0 {
		decimals = len(s) - i - 1
	}
	return v, decimals, err == nil
}

// computed reports the calculation of the answer that gives figure from two
// operands both found in the sources, as the answer wrote it.
func computed(answer, figure string, sources []source) (string, bool) {
	want, decimals, ok := figureValue(figure)
	if !ok {
		return "", false
	}
	tolerance := 0.5*math.Pow(10, -float64(decimals)) + 1e-9
	for _, m := range operationPattern.FindAllStringSubmatch(answer, -1) {
		a, _, okA := figureValue(m[1])
		b, _, okB := figureValue(m[3])
		if !okA || !okB {
			continue
		}
		var got float64
		switch m[2] {
		case "×", "x", "*":
			got = a * b
		case "+":
			got = a + b
		case "-", "−":
			got = a - b
		case "/", "÷":
			if b == 0 {
				continue
			}
			got = a / b
		}
		if math.Abs(got-want) > tolerance {
			continue
		}
		if operandFound(m[1], sources) && operandFound(m[3], sources) {
			return strings.TrimSpace(m[0]) + " = " + figure, true
		}
	}
	return "", false
}

func operandFound(raw string, sources []source) bool {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, raw)
	if len(digits) < 2 {
		return true
	}
	_, ok := locate(digits, sources)
	return ok
}

func checkFigures(answer string, sources []source) FigureCheck {
	figures := answerFigures(answer)
	if len(figures) == 0 || len(sources) == 0 {
		return FigureCheck{}
	}
	check := FigureCheck{Checked: len(figures), Unsourced: []string{}, Evidence: []FigureEvidence{}}
	for _, f := range figures {
		digits, _ := figureDigits(f)
		if ev, ok := locate(digits, sources); ok {
			ev.Figure = f
			check.Sourced++
			check.Evidence = append(check.Evidence, ev)
			continue
		}
		if formula, ok := computed(answer, f, sources); ok {
			check.Sourced++
			check.Evidence = append(check.Evidence, FigureEvidence{Figure: f, Formula: formula})
			continue
		}
		check.Unsourced = append(check.Unsourced, f)
	}
	return check
}

var webRefPattern = regexp.MustCompile(`\bW\d+\b`)

// citations lists the pages the answer rests on, in the order it cites them:
// the refs it wrote, then the pages its figures were found on.
func citations(answer string, sources []source, check FigureCheck) []Citation {
	byRef := map[string]source{}
	for _, s := range sources {
		if s.Ref != "" {
			if _, seen := byRef[s.Ref]; !seen {
				byRef[s.Ref] = s
			}
		}
	}
	var out []Citation
	add := func(ref string) {
		s, ok := byRef[ref]
		if !ok || slices.ContainsFunc(out, func(c Citation) bool { return c.Ref == ref }) {
			return
		}
		out = append(out, Citation{Ref: s.Ref, Title: s.Title, URL: s.URL})
	}
	for _, ref := range webRefPattern.FindAllString(answer, -1) {
		add(ref)
	}
	for _, ev := range check.Evidence {
		add(ev.Ref)
	}
	return out
}

const unsourcedNotice = "These figures in your answer appear in none of the sources you read this turn: %s. Correct each one from the sources or remove it. A figure you calculate is fine: write the calculation with the sourced figures, as 50 × 2,372 € = 118,60 €. Give the whole answer again, without commenting on this check."

func unsourcedMessage(figures []string) string {
	return fmt.Sprintf(unsourcedNotice, strings.Join(figures, ", "))
}
