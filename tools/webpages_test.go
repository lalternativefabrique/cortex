package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

type callingClient struct {
	steps []agent.CompletionResponse
	calls int
	seen  []string
}

func (c *callingClient) Complete(_ context.Context, req agent.CompletionRequest) (agent.CompletionResponse, error) {
	if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == agent.RoleTool {
		c.seen = append(c.seen, req.Messages[n-1].Content)
	}
	c.calls++
	if c.calls > len(c.steps) {
		return agent.CompletionResponse{Text: "done"}, nil
	}
	return c.steps[c.calls-1], nil
}

func call(id, name, args string) agent.CompletionResponse {
	return agent.CompletionResponse{ToolCalls: []agent.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(args)}}}
}

func pagesOf(t *testing.T, content string) []WebPage {
	t.Helper()
	var pages []WebPage
	if err := json.NewDecoder(strings.NewReader(content)).Decode(&pages); err != nil {
		t.Fatalf("result is not a JSON list of pages: %v\n%s", err, content)
	}
	return pages
}

func TestAPageKeepsItsRefAcrossTheToolsOfARun(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{
		{Title: "A", URL: "https://a.test", Content: "a"},
		{Title: "B", URL: "https://b.test", Content: "b"},
	}}
	fetcher := &stubFetcher{page: &Page{Title: "B", Text: "the whole of b"}}
	client := &callingClient{steps: []agent.CompletionResponse{
		call("1", "web_search", `{"query":"q"}`),
		call("2", "fetch_url", `{"url":"https://b.test"}`),
		call("3", "fetch_url", `{"url":"https://c.test"}`),
	}}
	r, err := agent.NewRunner(agent.Config{
		Client: client,
		Tools:  []agent.Tool{NewWebSearch(WebSearchConfig{Searcher: searcher}), NewFetchURL(FetchURLConfig{Fetcher: fetcher})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []agent.Message{{Role: agent.RoleUser, Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	if len(client.seen) != 3 {
		t.Fatalf("saw %d tool results, want 3", len(client.seen))
	}
	search := pagesOf(t, client.seen[0])
	if len(search) != 2 || search[0].Ref != "W1" || search[1].Ref != "W2" || search[1].URL != "https://b.test" {
		t.Fatalf("search pages = %+v", search)
	}
	if again := pagesOf(t, client.seen[1]); again[0].Ref != "W2" || again[0].Excerpt != "the whole of b" {
		t.Fatalf("a fetched page web_search returned got %+v, want W2 with its text", again[0])
	}
	if fresh := pagesOf(t, client.seen[2]); fresh[0].Ref != "W3" {
		t.Fatalf("a new page got %q, want W3", fresh[0].Ref)
	}
}

func TestWebPagesAreNumberedOutsideARun(t *testing.T) {
	searcher := &stubSearcher{results: []SearchResult{{Title: "A", URL: "https://a.test"}, {Title: "B", URL: "https://b.test"}}}
	res := runToolOK(t, NewWebSearch(WebSearchConfig{Searcher: searcher}), `{"query":"q"}`)
	if pages := pagesOf(t, res.Content); pages[0].Ref != "W1" || pages[1].Ref != "W2" {
		t.Fatalf("pages = %+v", pages)
	}
}
