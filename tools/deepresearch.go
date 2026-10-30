package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// DeepResearchConfig configures the deep_research tool.
type DeepResearchConfig struct {
	Client agent.Client
	// Tools are what the researcher reads the world with: web_search,
	// fetch_url, research, explore_site. A deep_research among them is
	// dropped, so a researcher never starts another one.
	Tools []agent.Tool
	// MaxSteps and MaxInputTokens bound one research. Zero means
	// DefaultDeepResearchSteps and DefaultDeepResearchInputTokens: dozens of
	// pages re-sent on every step make an unbounded research the most
	// expensive call an agent can make.
	MaxSteps       int
	MaxInputTokens int
	ContextWindow  int
	// OnUsage receives what the research spent, for a caller that counts the
	// turn's tokens: the research calls the model through Client, outside
	// the run that called the tool.
	OnUsage func(agent.Usage)
}

const (
	DefaultDeepResearchSteps       = 40
	DefaultDeepResearchInputTokens = 400_000
)

// DeepResearchMethod is the researcher's system prompt.
const DeepResearchMethod = `You research one question in depth on the web and write a report on it.

1. Plan. Split the question into the few sub-questions a complete answer needs.
2. Search each sub-question. Prefer research, which reads the sources; use web_search for a list of links, fetch_url to read one page, explore_site to go through one site. Search again with sharper words when a result is thin, and look for a second source for every claim that matters.
3. Check. When sources disagree, say so and say which one you trust and why. A figure you could not find in a page you read is left out, not guessed.
4. Report. A short answer first, then one section per sub-question, then what stays uncertain. Cite every page you use by its ref, as the tools give it.

Stop searching when every sub-question is answered or when another search would only repeat what you already read. Write the report in the language of the question.`

type deepResearchArgs struct {
	Question string `json:"question" jsonschema:"description=The question to research, in full, with everything from the conversation that matters to it: the researcher sees nothing else."`
}

type deepResearchTool struct {
	cfg DeepResearchConfig
}

// NewDeepResearch returns a tool that hands one question to a researcher: a
// run of its own, with its own context, that plans, reads dozens of pages
// and returns a cited report. The pages it cites keep their refs in the
// calling run, which shares them.
func NewDeepResearch(cfg DeepResearchConfig) agent.Tool {
	kept := make([]agent.Tool, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		if t.Name() != "deep_research" {
			kept = append(kept, t)
		}
	}
	cfg.Tools = kept
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = DefaultDeepResearchSteps
	}
	if cfg.MaxInputTokens == 0 {
		cfg.MaxInputTokens = DefaultDeepResearchInputTokens
	}
	return &deepResearchTool{cfg: cfg}
}

func (t *deepResearchTool) Name() string { return "deep_research" }

func (t *deepResearchTool) Description() string {
	return strings.Join([]string{
		"Research a question in depth: a researcher plans it, reads dozens of web pages, cross-checks them and returns a cited report.",
		"",
		"It takes minutes and many times the tokens of a search. Use it only when the person asks for a thorough answer, a comparison, a state of the art or a report, or when one research call would clearly not cover the question. For a fact, a definition or a quick check, use research or web_search.",
		"",
		"Give the person the report itself, its refs included as they are: do not rewrite it from memory, and do not add what it does not say.",
	}, "\n")
}

func (t *deepResearchTool) InputSchema() any { return deepResearchArgs{} }

func (t *deepResearchTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args deepResearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		return failure("question is required")
	}
	if t.cfg.Client == nil || len(t.cfg.Tools) == 0 {
		return failure("no researcher is configured")
	}
	sources := make([]string, 0, len(t.cfg.Tools))
	for _, tool := range t.cfg.Tools {
		sources = append(sources, tool.Name())
	}
	runner, err := agent.NewRunner(agent.Config{
		Client:         t.cfg.Client,
		Tools:          t.cfg.Tools,
		System:         DeepResearchMethod,
		MaxSteps:       t.cfg.MaxSteps,
		MaxInputTokens: t.cfg.MaxInputTokens,
		ContextWindow:  t.cfg.ContextWindow,
		CloseOnBound:   true,
		SourceTools:    sources,
	})
	if err != nil {
		return failure("%v", err)
	}
	res, err := runner.Run(ctx, []agent.Message{{Role: agent.RoleUser, Content: question}})
	if t.cfg.OnUsage != nil {
		t.cfg.OnUsage(res.Usage)
	}
	if err != nil {
		return failure("%v", err)
	}
	report := strings.TrimSpace(res.Text)
	if report == "" {
		return failure("the research ended without a report")
	}
	if read := pagesRead(res.ToolCalls); len(read) > 0 && !citedRef.MatchString(report) {
		cited, usage, err := t.cite(ctx, question, report, read)
		if t.cfg.OnUsage != nil {
			t.cfg.OnUsage(usage)
		}
		if err == nil && citedRef.MatchString(cited) {
			report = cited
		}
	}
	return agent.ToolResult{
		Content: report,
		Metadata: map[string]any{
			"ok":         true,
			"steps":      res.Steps,
			"tool_calls": len(res.ToolCalls),
			"bounded":    res.Truncated || res.Exhausted || res.Stalled,
		},
	}, nil
}

var (
	citedRef = regexp.MustCompile(`\[W\d+\]`)
	pageRef  = regexp.MustCompile(`"ref":"(W\d+)","title":"((?:[^"\\]|\\.)*)","url":"((?:[^"\\]|\\.)*)"`)
)

type readPage struct{ ref, title, url string }

func pagesRead(calls []agent.ToolCallTrace) []readPage {
	seen := map[string]bool{}
	var out []readPage
	for _, c := range calls {
		for _, m := range pageRef.FindAllStringSubmatch(c.Result, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, readPage{ref: m[1], title: m[2], url: m[3]})
			}
		}
	}
	return out
}

// cite asks the researcher once, without tools, to found its report on the
// pages it read: a report without refs cannot be checked by the person.
func (t *deepResearchTool) cite(ctx context.Context, question, report string, read []readPage) (string, agent.Usage, error) {
	var b strings.Builder
	b.WriteString("Your report cites no page. Rewrite it so that every claim carries the ref of the page that supports it, in brackets, as [W1]. Remove every claim no page below supports. Pages you read:\n")
	for _, p := range read {
		fmt.Fprintf(&b, "%s: %s %s\n", p.ref, p.title, p.url)
	}
	runner, err := agent.NewRunner(agent.Config{Client: t.cfg.Client, System: DeepResearchMethod, MaxSteps: 1})
	if err != nil {
		return "", agent.Usage{}, err
	}
	res, err := runner.Run(ctx, []agent.Message{
		{Role: agent.RoleUser, Content: question},
		{Role: agent.RoleAssistant, Content: report},
		{Role: agent.RoleUser, Content: b.String()},
	})
	return strings.TrimSpace(res.Text), res.Usage, err
}
