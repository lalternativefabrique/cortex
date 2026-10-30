package tools

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

type researcherClient struct {
	mu       sync.Mutex
	requests []agent.CompletionRequest
	replies  []agent.CompletionResponse
}

func (c *researcherClient) Complete(_ context.Context, req agent.CompletionRequest) (agent.CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, req)
	if len(c.requests) > len(c.replies) {
		return agent.CompletionResponse{Text: "out of script"}, nil
	}
	return c.replies[len(c.requests)-1], nil
}

type pageTool struct {
	name  string
	calls int
}

func (p *pageTool) Name() string        { return p.name }
func (p *pageTool) Description() string { return "reads the web" }
func (p *pageTool) InputSchema() any    { return struct{}{} }
func (p *pageTool) Execute(context.Context, json.RawMessage) (agent.ToolResult, error) {
	p.calls++
	return agent.ToolResult{Content: "Rain fell 12 mm in Lyon on Monday."}, nil
}

func TestDeepResearchRunsAResearcherOfItsOwn(t *testing.T) {
	search := &pageTool{name: "web_search"}
	client := &researcherClient{replies: []agent.CompletionResponse{
		{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "web_search", Arguments: json.RawMessage(`{}`)}}, Usage: agent.Usage{Input: 100, Output: 10}},
		{Text: "Lyon had 12 mm of rain on Monday [W1].", Usage: agent.Usage{Input: 200, Output: 20}},
	}}
	var spent agent.Usage
	tool := NewDeepResearch(DeepResearchConfig{
		Client:  client,
		Tools:   []agent.Tool{search, &pageTool{name: "deep_research"}},
		OnUsage: func(u agent.Usage) { spent.Add(u) },
	})

	res := runToolOK(t, tool, `{"question":"How much rain fell in Lyon on Monday?"}`)

	if !strings.Contains(res.Content, "12 mm") {
		t.Fatalf("the report is the researcher's answer, got %q", res.Content)
	}
	if search.calls != 1 {
		t.Fatalf("the researcher searches with the tools it was given, web_search ran %d times", search.calls)
	}
	first := client.requests[0]
	if first.System != DeepResearchMethod {
		t.Fatal("the researcher works under the research method")
	}
	if len(first.Messages) != 1 || !strings.Contains(first.Messages[0].Content, "Lyon") {
		t.Fatalf("the researcher sees the question alone, got %+v", first.Messages)
	}
	for _, offered := range first.Tools {
		if offered.Name() == "deep_research" {
			t.Fatal("a researcher must not start another research")
		}
	}
	if spent.Input != 300 || spent.Output != 30 {
		t.Fatalf("the caller is told what the research spent, got %+v", spent)
	}
}

func TestDeepResearchNeedsAQuestionAndAResearcher(t *testing.T) {
	tool := NewDeepResearch(DeepResearchConfig{Client: &researcherClient{}, Tools: []agent.Tool{&pageTool{name: "web_search"}}})
	if res := runToolOK(t, tool, `{"question":"  "}`); res.Metadata["ok"] != false {
		t.Fatalf("an empty question is refused, got %q", res.Content)
	}
	bare := NewDeepResearch(DeepResearchConfig{Client: &researcherClient{}})
	if res := runToolOK(t, bare, `{"question":"q"}`); res.Metadata["ok"] != false {
		t.Fatalf("no tools to read the web with is refused, got %q", res.Content)
	}
}

type refPageTool struct{}

func (refPageTool) Name() string        { return "fetch_url" }
func (refPageTool) Description() string { return "reads a page" }
func (refPageTool) InputSchema() any    { return struct{}{} }
func (refPageTool) Execute(context.Context, json.RawMessage) (agent.ToolResult, error) {
	return agent.ToolResult{Content: `[{"ref":"W1","title":"Lyon weather","url":"https://meteo.test/lyon","excerpt":"12 mm"}]`}, nil
}

func TestAReportWithoutRefsIsAskedOnceToCite(t *testing.T) {
	client := &researcherClient{replies: []agent.CompletionResponse{
		{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "fetch_url", Arguments: json.RawMessage(`{}`)}}},
		{Text: "Lyon had 12 mm of rain."},
		{Text: "Lyon had 12 mm of rain [W1]."},
	}}
	tool := NewDeepResearch(DeepResearchConfig{Client: client, Tools: []agent.Tool{refPageTool{}}})

	res := runToolOK(t, tool, `{"question":"Rain in Lyon?"}`)

	if res.Content != "Lyon had 12 mm of rain [W1]." {
		t.Fatalf("the cited rewrite replaces the bare report, got %q", res.Content)
	}
	ask := client.requests[2]
	last := ask.Messages[len(ask.Messages)-1].Content
	if !strings.Contains(last, "W1: Lyon weather https://meteo.test/lyon") {
		t.Fatalf("the researcher is reminded of the pages it read, got %q", last)
	}
	if len(ask.Tools) != 0 {
		t.Fatal("the rewrite cites what was read, it does not search again")
	}
}

func TestACitedReportIsKeptAsItIs(t *testing.T) {
	client := &researcherClient{replies: []agent.CompletionResponse{
		{ToolCalls: []agent.ToolCall{{ID: "c1", Name: "fetch_url", Arguments: json.RawMessage(`{}`)}}},
		{Text: "Lyon had 12 mm of rain [W1]."},
	}}
	tool := NewDeepResearch(DeepResearchConfig{Client: client, Tools: []agent.Tool{refPageTool{}}})
	runToolOK(t, tool, `{"question":"Rain in Lyon?"}`)
	if len(client.requests) != 2 {
		t.Fatalf("a cited report needs no rewrite, the model was called %d times", len(client.requests))
	}
}
