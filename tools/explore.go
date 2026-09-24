package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// SiteExplorer lists a site's pages without reading them.
type SiteExplorer interface {
	// Explore returns the URLs reachable from start, within scope. It reads
	// structure, not content: what it costs is one crawl of the links, not a
	// render of every page.
	Explore(ctx context.Context, req ExploreRequest) ([]string, error)
}

// ExploreRequest is one site walk.
type ExploreRequest struct {
	URL string
	// MaxPages bounds the walk. Zero leaves the backend's own limit, which
	// is what keeps an unbounded site from becoming an unbounded call.
	MaxPages int
	// Include and Exclude are path fragments a URL must or must not carry.
	// They are how a walk stays on the part of a site that was asked about.
	Include []string
	Exclude []string
}

// ExploreConfig configures the explore_site tool.
type ExploreConfig struct {
	Explorer SiteExplorer
}

// exploreListed caps what one call puts in front of the model. A site map is
// read to choose a page to open, and a thousand URLs is not a choice.
const exploreListed = 100

type exploreArgs struct {
	URL     string   `json:"url" jsonschema:"description=The site or section to explore, as a full URL. Exploring from a section rather than the home page keeps the answer to the part that matters."`
	Include []string `json:"include,omitempty" jsonschema:"description=Only list URLs whose path contains one of these fragments — e.g. '/docs/' to stay in the documentation."`
	Exclude []string `json:"exclude,omitempty" jsonschema:"description=Skip URLs whose path contains one of these fragments — e.g. '/blog/' or a locale prefix."`
}

type exploreTool struct {
	cfg ExploreConfig
}

// NewExploreSite returns a tool that lists what a site holds.
func NewExploreSite(cfg ExploreConfig) agent.Tool {
	return &exploreTool{cfg: cfg}
}

func (t *exploreTool) Name() string { return "explore_site" }

func (t *exploreTool) Description() string {
	return strings.Join([]string{
		"List the pages a site holds, without reading them.",
		"",
		"Use it when the answer is somewhere on a site but you do not know which page — a documentation set, a reference, a section you were pointed at. Then open the promising ones with fetch_url.",
		"",
		"Returns URLs only. Narrow it with include/exclude rather than exploring a whole site: a large site is listed partially, and the part you get may not be the part you wanted.",
	}, "\n")
}

func (t *exploreTool) InputSchema() any { return exploreArgs{} }

func (t *exploreTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args exploreArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	url := strings.TrimSpace(args.URL)
	if url == "" {
		return failure("url is required")
	}
	if t.cfg.Explorer == nil {
		return failure("no site explorer is configured")
	}

	links, err := t.cfg.Explorer.Explore(ctx, ExploreRequest{
		URL:      url,
		MaxPages: exploreListed,
		Include:  args.Include,
		Exclude:  args.Exclude,
	})
	if err != nil {
		return failure("%v", err)
	}
	if len(links) == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("No pages found under %s.", url),
			Metadata: map[string]any{"ok": true, "pages": 0},
		}, nil
	}

	shown := links
	truncated := false
	if len(shown) > exploreListed {
		shown = shown[:exploreListed]
		truncated = true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d pages under %s:\n\n", len(shown), url)
	for _, link := range shown {
		fmt.Fprintf(&b, "%s\n", link)
	}
	if truncated {
		// Say the list was cut rather than letting it read as the whole site:
		// an answer founded on a partial map is founded on a partial site.
		fmt.Fprintf(&b, "\n(%d more not shown — narrow the walk with include/exclude)\n", len(links)-len(shown))
	}

	return agent.ToolResult{
		Content: strings.TrimRight(b.String(), "\n"),
		Metadata: map[string]any{
			"ok":        true,
			"pages":     len(shown),
			"truncated": truncated,
		},
	}, nil
}
