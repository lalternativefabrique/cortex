package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Handoff is what a different agent needs to take the work over with no
// other context. Task, Instructions and Repo are facts copied by code; the
// other sections are written by the model.
type Handoff struct {
	Task         string           `json:"task"`
	Instructions []string         `json:"instructions,omitempty"`
	Repo         *RepoState       `json:"repo,omitempty"`
	Code         string           `json:"code,omitempty"`
	Decisions    string           `json:"decisions,omitempty"`
	Failures     string           `json:"failures,omitempty"`
	Verification string           `json:"verification,omitempty"`
	Remaining    string           `json:"remaining,omitempty"`
	Questions    string           `json:"questions,omitempty"`
	Notes        string           `json:"notes,omitempty"`
	Memory       MemoryExtraction `json:"memory,omitzero"`
}

// WorkState is the host's view of the work in progress, read at handoff time.
type WorkState interface {
	State(ctx context.Context) (RepoState, error)
}

// RepoState is where the repository stands.
type RepoState struct {
	Branch   string   `json:"branch,omitempty"`
	Base     string   `json:"base,omitempty"`
	Commits  []string `json:"commits,omitempty"`
	Status   string   `json:"status,omitempty"`
	Diffstat string   `json:"diffstat,omitempty"`
}

const (
	headingTask         = "## 1. Task"
	headingInstructions = "## 2. Instructions received since the task"
	headingRepo         = "## 3. Repository state"
	headingCode         = "## 4. What is known of the code"
	headingDecisions    = "## 5. Decisions"
	headingFailures     = "## 6. What failed"
	headingVerification = "## 7. Verification state"
	headingRemaining    = "## 8. What remains"
	headingQuestions    = "## 9. Open questions"
	headingNotes        = "## Notes"
	handoffTitle        = "# Handoff"
	handoffClosing      = "The repository is the truth. Before editing a file, reread it and run `git diff`: this handoff may be wrong on the detail of a change."
)

// HandoffInstruction is the prompt that has a model write sections 4 to 9 of
// a Handoff from a transcript.
const HandoffInstruction = `You are writing a handoff: the document a different agent, possibly another model, reads to take this work over with no other context.

The task, every instruction received since, and the repository state are copied into the handoff by code. Do not restate them. Write exactly these six sections, with these headings, in this order:

` + headingCode + `
Files, functions, types and structure that matter, and why each matters.

` + headingDecisions + `
Each decision taken, its reason, and the alternatives set aside.

` + headingFailures + `
What was tried and failed, with the error text, so it is not retried.

` + headingVerification + `
The last build, test and CI runs and their outcome. Copy failures verbatim.

` + headingRemaining + `
What is left to do. End with the next concrete action.

` + headingQuestions + `
What is still undecided, and who it waits on.

Be precise: name files, functions and errors. Do not narrate which tools ran. Write "None." under a section with nothing to say. No preamble.

If the transcript contains decisions, constraints or facts worth preserving exactly, also call record_memory with them.`

// Render writes the handoff as Markdown. ParseHandoff reads it back.
func (h Handoff) Render() string {
	var b strings.Builder
	b.WriteString(handoffTitle + "\n\n")
	writeSection(&b, headingTask, h.Task)
	var instructions strings.Builder
	for i, text := range h.Instructions {
		fmt.Fprintf(&instructions, "### Instruction %d\n\n%s\n\n", i+1, strings.TrimSpace(text))
	}
	writeSection(&b, headingInstructions, instructions.String())
	writeSection(&b, headingRepo, h.Repo.render())
	b.WriteString(h.modelSections())
	b.WriteString("---\n\n" + handoffClosing + "\n")
	return b.String()
}

func (h Handoff) modelSections() string {
	var b strings.Builder
	if strings.TrimSpace(h.Notes) != "" {
		writeSection(&b, headingNotes, h.Notes)
	}
	writeSection(&b, headingCode, joinBlocks(h.Code, recorded(MemoryExtraction{Facts: h.Memory.Facts})))
	writeSection(&b, headingDecisions, joinBlocks(h.Decisions, recorded(MemoryExtraction{Decisions: h.Memory.Decisions, Constraints: h.Memory.Constraints})))
	writeSection(&b, headingFailures, h.Failures)
	writeSection(&b, headingVerification, h.Verification)
	writeSection(&b, headingRemaining, h.Remaining)
	writeSection(&b, headingQuestions, h.Questions)
	return b.String()
}

func writeSection(b *strings.Builder, heading, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		body = "None."
	}
	b.WriteString(heading + "\n\n" + body + "\n\n")
}

func recorded(m MemoryExtraction) string {
	if m.IsEmpty() {
		return ""
	}
	return "Recorded during the work:\n\n" + m.Render()
}

func joinBlocks(blocks ...string) string {
	var kept []string
	for _, b := range blocks {
		if b = strings.TrimSpace(b); b != "" && b != "None." {
			kept = append(kept, b)
		}
	}
	return strings.Join(kept, "\n\n")
}

func (r *RepoState) render() string {
	if r == nil {
		return "Not collected: the host reported no repository state."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- Branch: %s\n", orNone(r.Branch))
	fmt.Fprintf(&b, "- Base: %s\n", orNone(r.Base))
	block := func(title, body string) {
		fmt.Fprintf(&b, "\n%s:\n", title)
		if strings.TrimSpace(body) == "" {
			b.WriteString("None.\n")
			return
		}
		b.WriteString("```\n" + strings.TrimRight(body, "\n") + "\n```\n")
	}
	block("Commits since base", strings.Join(r.Commits, "\n"))
	block("Uncommitted changes (git status --porcelain)", r.Status)
	block("Diffstat against base", r.Diffstat)
	return b.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

var instructionHeading = regexp.MustCompile(`(?m)^### Instruction \d+\s*$`)

// ParseHandoff reads back what Render wrote. A document without the
// handoff's headings is taken whole as the task. The repository state is not
// read back: the receiving host collects its own.
func ParseHandoff(markdown string) Handoff {
	sections := splitSections(markdown, []string{
		headingTask, headingInstructions, headingRepo, headingNotes, headingCode, headingDecisions,
		headingFailures, headingVerification, headingRemaining, headingQuestions,
	})
	task, ok := sections[headingTask]
	if !ok {
		return Handoff{Task: strings.TrimSpace(markdown)}
	}
	h := Handoff{Task: task}
	h.Instructions = splitInstructions(sections[headingInstructions])
	h.fillModelSections(sections)
	h.Notes = noneToEmpty(sections[headingNotes])
	return h
}

func splitInstructions(body string) []string {
	if body == "" || body == "None." {
		return nil
	}
	parts := instructionHeading.Split(body, -1)
	var out []string
	for _, p := range parts[1:] {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func (h *Handoff) fillModelSections(sections map[string]string) {
	h.Code = noneToEmpty(sections[headingCode])
	h.Decisions = noneToEmpty(sections[headingDecisions])
	h.Failures = noneToEmpty(sections[headingFailures])
	h.Verification = noneToEmpty(sections[headingVerification])
	h.Remaining = noneToEmpty(sections[headingRemaining])
	h.Questions = noneToEmpty(sections[headingQuestions])
}

// parseModelSections reads the six sections HandoffInstruction asks for.
// Text the model wrote outside them is kept as Notes rather than lost.
func parseModelSections(text string) Handoff {
	headings := []string{headingCode, headingDecisions, headingFailures, headingVerification, headingRemaining, headingQuestions}
	sections := splitSections(text, headings)
	var h Handoff
	h.fillModelSections(sections)
	h.Notes = sections[""]
	return h
}

func noneToEmpty(s string) string {
	if s == "None." {
		return ""
	}
	return s
}

// splitSections cuts text on lines equal to one of headings. Text before the
// first heading is keyed "". The handoff's closing rule ends the last section.
func splitSections(text string, headings []string) map[string]string {
	canonical := map[string]string{}
	for _, h := range headings {
		canonical[strings.ToLower(h)] = h
	}
	out := map[string]string{}
	current := ""
	var body strings.Builder
	flush := func() {
		if s := strings.TrimSpace(body.String()); s != "" || current != "" {
			out[current] = s
		}
		body.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.ToLower(strings.TrimSpace(line))
		if heading, ok := canonical[trimmed]; ok {
			flush()
			current = heading
			continue
		}
		if current == "" && trimmed == strings.ToLower(handoffTitle) {
			continue
		}
		if strings.TrimSpace(line) == handoffClosing {
			continue
		}
		body.WriteString(line + "\n")
	}
	flush()
	for k, v := range out {
		out[k] = strings.TrimSpace(strings.TrimSuffix(v, "---"))
	}
	return out
}

// HandoffOf returns the handoff a compaction left in messages, the latest
// one when there are several.
func HandoffOf(messages []Message) (Handoff, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Handoff != nil {
			return *messages[i].Handoff, true
		}
	}
	return Handoff{}, false
}

// HandoffCallback is the optional part of a Callback that receives the
// handoff a compaction produced, before OnCompactEnd.
type HandoffCallback interface {
	OnHandoff(Handoff)
}
