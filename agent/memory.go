package agent

import "strings"

// MemoryExtraction is what the compactor asks the model to produce as a
// typed alternative to prose, so repeated compaction does not re-summarise
// an already-summarised text.
type MemoryExtraction struct {
	Decisions   []string `json:"decisions,omitempty" jsonschema:"description=Choices made and why, stated so they are not revisited."`
	Constraints []string `json:"constraints,omitempty" jsonschema:"description=Limits discovered: what must not be done, or does not work."`
	Facts       []string `json:"facts,omitempty" jsonschema:"description=Established facts about the codebase: file paths, symbols, structure."`
}

// Render turns the extraction into text, the same shape used for the
// RoleMemory message's content.
func (m MemoryExtraction) Render() string {
	var b strings.Builder
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(title)
		b.WriteString(":\n")
		for _, item := range items {
			b.WriteString("- ")
			b.WriteString(item)
			b.WriteByte('\n')
		}
	}
	section("Decisions", m.Decisions)
	section("Constraints", m.Constraints)
	section("Facts", m.Facts)
	return b.String()
}

// IsEmpty reports whether nothing was extracted.
func (m MemoryExtraction) IsEmpty() bool {
	return len(m.Decisions) == 0 && len(m.Constraints) == 0 && len(m.Facts) == 0
}
