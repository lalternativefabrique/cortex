package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

type stubSearcher struct {
	got     SearchQuery
	results []SearchResult
	err     error
}

func (s *stubSearcher) Search(_ context.Context, q SearchQuery) ([]SearchResult, error) {
	s.got = q
	return s.results, s.err
}

func runToolOK(t *testing.T, tool agent.Tool, args string) agent.ToolResult {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res
}

// research is worth a tool only because it asks for the pages themselves: a
// search that returns excerpts is what web_search already does.
func TestResearchAsksForPageContent(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{
		{Title: "A study", URL: "https://example.test/a", Markdown: "# Findings\nRain falls."},
	}}
	tool := NewResearch(ResearchConfig{Searcher: searcher})

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"does rain fall?"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if searcher.got.WithContent <= 0 {
		t.Errorf("WithContent is %d, want the pages read", searcher.got.WithContent)
	}
	if searcher.got.ContentRunes <= 0 {
		t.Errorf("ContentRunes is %d, want each page bounded", searcher.got.ContentRunes)
	}
	if !strings.Contains(res.Content, "Rain falls") {
		t.Errorf("content is %q, want the source's own text", res.Content)
	}
	if !strings.Contains(res.Content, "https://example.test/a") {
		t.Error("the source URL is missing — an answer that cannot be traced is not founded")
	}
}

func TestResearchCarriesScope(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{{Title: "t", URL: "u", Text: "x"}}}
	tool := NewResearch(ResearchConfig{Searcher: searcher})

	runToolOK(t, tool, `{"question":"what does the research say?","scope":"studies"}`)
	if searcher.got.Scope != ScopeStudies {
		t.Errorf("scope is %q, want studies", searcher.got.Scope)
	}
}

// A page that refused to be read is not a page that said nothing, and the
// model has to be able to tell the two apart.
func TestResearchReportsUnreadSources(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{
		{Title: "Readable", URL: "https://example.test/a", Text: "here it is"},
		{Title: "Blocked", URL: "https://example.test/b", ContentError: "upstream refused"},
	}}
	tool := NewResearch(ResearchConfig{Searcher: searcher})

	res := runToolOK(t, tool, `{"question":"q"}`)
	if !strings.Contains(res.Content, "upstream refused") {
		t.Errorf("content is %q, want it to say why a source is missing", res.Content)
	}
	if got := res.Metadata["read"]; got != 1 {
		t.Errorf("read is %v, want 1 — only one source answered with a body", got)
	}
}

func TestResearchWithoutSourcesSaysSo(t *testing.T) {
	tool := NewResearch(ResearchConfig{Searcher: &stubSearcher{}})

	res := runToolOK(t, tool, `{"question":"an unanswerable question"}`)
	if !strings.Contains(res.Content, "No sources") {
		t.Errorf("content is %q, want it to report finding nothing", res.Content)
	}
}

func TestResearchNeedsAQuestion(t *testing.T) {
	tool := NewResearch(ResearchConfig{Searcher: &stubSearcher{}})

	res := runToolOK(t, tool, `{"question":"  "}`)
	if res.Metadata["ok"] != false {
		t.Errorf("an empty question was accepted: %+v", res)
	}
}

// A backend failure reaches the model as a result it can react to, not as an
// error that ends the run.
func TestResearchBackendFailureIsAResult(t *testing.T) {
	tool := NewResearch(ResearchConfig{Searcher: &stubSearcher{err: errors.New("backend down")}})

	res := runToolOK(t, tool, `{"question":"q"}`)
	if !strings.Contains(res.Content, "backend down") {
		t.Errorf("content is %q, want the cause", res.Content)
	}
}

// The scope the model asks for reaches the backend as an intent, so a
// question about research is not answered by a blog post summarising it.
func TestWebSearchCarriesScope(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{{Title: "t", URL: "u"}}}
	tool := NewWebSearch(WebSearchConfig{Searcher: searcher})

	runToolOK(t, tool, `{"query":"what does the research say","scope":"studies"}`)
	if searcher.got.Scope != ScopeStudies {
		t.Errorf("scope is %q, want studies", searcher.got.Scope)
	}
}

// A local business lookup has no scholarly answer whatever the model asked
// for, so the tool overrides rather than trusting the argument.
func TestWebSearchLocalQueryOverridesScope(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{{Title: "t", URL: "u"}}}
	tool := NewWebSearch(WebSearchConfig{Searcher: searcher})

	runToolOK(t, tool, `{"query":"bakery","local_business_tag":"shop=bakery","scope":"studies"}`)
	if searcher.got.Scope != ScopeLocal {
		t.Errorf("scope is %q, want local", searcher.got.Scope)
	}
}

// An unqualified question searches everything, which is what it deserves and
// what the chat already did before scope existed.
func TestWebSearchDefaultsToTheWholeWeb(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{{Title: "t", URL: "u"}}}
	tool := NewWebSearch(WebSearchConfig{Searcher: searcher})

	runToolOK(t, tool, `{"query":"anything"}`)
	if searcher.got.Scope != ScopeWeb {
		t.Errorf("scope is %q, want the default", searcher.got.Scope)
	}
}
