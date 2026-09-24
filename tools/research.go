package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// ResearchConfig configures the research tool.
type ResearchConfig struct {
	// Searcher must be able to read pages into its answer (SearchQuery's
	// WithContent) for this tool to be worth offering: without it, research
	// is web_search followed by fetch_url, which the model can already do.
	Searcher Searcher
}

const (
	// researchResults is how many sources one question is founded on. The
	// backend reads every one of them, so this is a cost as much as a
	// breadth: enough to disagree with each other, few enough to fit.
	researchResults = 5
	// researchRunes bounds each source. A page contributes a section, not a
	// book — five long articles at full length would crowd out everything
	// else the turn holds.
	researchRunes = 3000
)

type researchArgs struct {
	Question string `json:"question" jsonschema:"description=The question to answer, in full. Not keywords: the sources are read to answer this, so a precise question is read precisely."`
	Scope    string `json:"scope,omitempty" jsonschema:"description='studies' to found the answer on scholarly sources — papers\\, journals\\, preprints. 'recent' when only the last few weeks count. Leave empty otherwise.,enum=studies,enum=recent"`
}

type researchTool struct {
	cfg ResearchConfig
}

// NewResearch returns a tool that searches and reads the sources in one call.
func NewResearch(cfg ResearchConfig) agent.Tool {
	return &researchTool{cfg: cfg}
}

func (t *researchTool) Name() string { return "research" }

func (t *researchTool) Description() string {
	return strings.Join([]string{
		"Answer a question from the web, reading the sources rather than their excerpts.",
		"",
		"Searches, then reads the top results' pages and returns their text. One call where web_search followed by several fetch_url would otherwise be needed — so prefer it whenever the answer has to be founded on what pages actually say, rather than on what a search excerpt hints at.",
		"",
		"Use web_search instead when a list of links is what is wanted, or when the excerpts are enough.",
	}, "\n")
}

func (t *researchTool) InputSchema() any { return researchArgs{} }

func (t *researchTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args researchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		return failure("question is required")
	}
	if t.cfg.Searcher == nil {
		return failure("no search backend is configured")
	}

	results, err := t.cfg.Searcher.Search(ctx, SearchQuery{
		Query:        question,
		Scope:        SearchScope(strings.TrimSpace(args.Scope)),
		MaxResults:   researchResults,
		WithContent:  researchResults,
		ContentRunes: researchRunes,
	})
	if err != nil {
		return failure("%v", err)
	}
	if len(results) == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("No sources found for %q.", question),
			Metadata: map[string]any{"ok": true, "sources": 0},
		}, nil
	}

	var b strings.Builder
	read := 0
	for i, r := range results {
		fmt.Fprintf(&b, "## %d. %s\n%s\n", i+1, firstNonEmpty(r.Title, r.URL), r.URL)
		text := strings.TrimSpace(firstNonEmpty(r.Markdown, r.Text))
		switch {
		case text != "":
			read++
			fmt.Fprintf(&b, "\n%s\n", text)
		case strings.TrimSpace(r.ContentError) != "":
			// Say why a source is missing rather than letting it look thin:
			// a page that refused to be read is not a page that said nothing.
			fmt.Fprintf(&b, "\n(not read: %s)\n", strings.TrimSpace(r.ContentError))
		case strings.TrimSpace(r.Content) != "":
			fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(r.Content))
		}
		b.WriteString("\n")
	}

	return agent.ToolResult{
		Content: strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]any{
			"ok":      true,
			"sources": len(results),
			// read says how many of them answered with a body, which is what
			// separates a founded answer from a list of titles.
			"read": read,
		},
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
