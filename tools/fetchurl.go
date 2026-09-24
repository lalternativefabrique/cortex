package tools

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// DefaultMaxRunes caps what one page contributes to the model's context. A
// long article otherwise crowds out every other source gathered this turn.
const DefaultMaxRunes = 6000

// PageFetcher reads one URL's main text.
type PageFetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (*Page, error)
}

// FetchRequest is one page read.
type FetchRequest struct {
	URL string
	// MaxRunes bounds the text returned. Zero means DefaultMaxRunes.
	MaxRunes int
	// Render retries a page that yields nothing statically in a browser. It
	// costs seconds and a rendering engine, so it is asked for rather than
	// assumed — but a client-side-rendered page returns empty without it.
	Render bool
}

// FetchURLConfig configures the fetch_url tool.
type FetchURLConfig struct {
	Fetcher PageFetcher
}

type fetchURLArgs struct {
	URL    string `json:"url" jsonschema:"description=The full URL to read, such as one returned by web_search."`
	Render bool   `json:"render,omitempty" jsonschema:"description=Run the page's JavaScript before reading it. Slower, so leave it off first — then set it when a read came back empty and the page is an app rather than a document."`
}

type fetchURLTool struct {
	cfg FetchURLConfig
}

// NewFetchURL returns a tool that reads a web page's main text through
// cfg.Fetcher.
func NewFetchURL(cfg FetchURLConfig) agent.Tool {
	return &fetchURLTool{cfg: cfg}
}

func (t *fetchURLTool) Name() string { return "fetch_url" }

func (t *fetchURLTool) Description() string {
	return strings.Join([]string{
		"Download a web page and extract its main text.",
		"",
		"Call this on a URL returned by web_search to read past its short excerpt — the excerpt is often too short to found an answer on.",
		"",
		"Does not execute JavaScript: a page whose content is rendered client-side may come back empty.",
	}, "\n")
}

func (t *fetchURLTool) InputSchema() any { return fetchURLArgs{} }

func (t *fetchURLTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args fetchURLArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	url := strings.TrimSpace(args.URL)
	if url == "" {
		return failure("url is required")
	}
	if t.cfg.Fetcher == nil {
		return failure("no page-fetching backend is configured")
	}

	page, err := t.cfg.Fetcher.Fetch(ctx, FetchRequest{
		URL:      url,
		MaxRunes: DefaultMaxRunes,
		Render:   args.Render,
	})
	if err != nil {
		return failure("%v", err)
	}
	if strings.TrimSpace(page.Text) == "" {
		if args.Render {
			return agent.ToolResult{
				Content:  "The page yielded no readable text even rendered — it may block automated readers, or hold no text at all.",
				Metadata: map[string]any{"ok": true, "empty": true, "rendered": true},
			}, nil
		}
		return agent.ToolResult{
			Content:  "The page yielded no readable text. If it builds its content with JavaScript, call this again with render set.",
			Metadata: map[string]any{"ok": true, "empty": true},
		}, nil
	}

	return agent.ToolResult{
		Content:  page.Title + "\n\n" + page.Text,
		Metadata: map[string]any{"ok": true, "title": page.Title, "chars": len(page.Text)},
	}, nil
}
