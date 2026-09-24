// Package websearch queries the platform's search for web results.
//
// Preferably through tornade, the HTTP facade the platform's search backends
// sit behind: it owns the general/academic fusion and backs the general
// category with Brave when a self-hosted SearXNG's engines are rate-limited
// or blocked. A caller reaching SearXNG directly gets neither.
//
// SearXNG stays as the direct fallback for a deployment with no tornade
// reachable. Either way there is no API key and no per-query cost, and a
// deployment with neither configured simply omits the tool, the same way
// vision is omitted without a model.
//
// Built on github.com/lalternative/packages/go/search, which also backs
// Synthiz's own access — the general/academic fusion and RRF scoring are the
// same code, not a reimplementation.
package websearch

import (
	"context"
	"fmt"
	"github.com/lalternative/packages/go/cortex/tools"
	"net/http"
	"strings"
	"time"

	pkgsearch "github.com/lalternative/packages/go/search"
	"github.com/lalternative/packages/go/search/searxng"
	tornad "github.com/lalternative/packages/tornad/sdk-go"
)

// Config points at the backend to query.
type Config struct {
	// TornadURL points at a tornad instance. When set it wins over BaseURL:
	// tornad owns the category fusion and backs the general category with
	// Brave, which is the cover a self-hosted SearXNG lacks when an upstream
	// engine is rate-limited or blocked.
	TornadURL string
	// TornadKey authenticates calls to TornadURL. Empty sends nothing, which
	// an internal-only tornad accepts.
	TornadKey string
	// BaseURL points straight at a SearXNG instance, which is what a
	// deployment with no tornad reachable still has.
	BaseURL string
	// Header, when set, is added to every request — the desktop proxies
	// through core's own /search endpoint and authenticates with it the same
	// way it authenticates every other call to core.
	Header     http.Header
	Timeout    time.Duration
	HTTPClient *http.Client
}

// Configured reports whether there is any backend to search with. An empty
// config leaves web_search out entirely rather than offering a tool that
// errors on every call.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.TornadURL) != "" || strings.TrimSpace(c.BaseURL) != ""
}

const (
	DefaultTimeout    = 15 * time.Second
	DefaultMaxResults = 8
	// mergeDeadline caps how long a Search call waits for the academic
	// category before returning with only the general category's results.
	// Measured against Synthiz's own instance, academic engines answer in
	// 6-8s against ~1s for general — waiting for both on every call would
	// make every search feel as slow as the worst category.
	mergeDeadline = 4 * time.Second
	// recentRange is what ScopeRecent asks tornad for. A month is the
	// shortest window that still answers "what happened lately" for a topic
	// nobody wrote about this week.
	recentRange = "month"
)

// Client queries the platform's search, through tornad when there is one and
// straight at SearXNG otherwise.
type Client struct {
	// tornad, when set, answers every search: it fuses the categories itself,
	// so the per-category providers below stay nil.
	tornad *tornad.Client

	general  pkgsearch.Provider
	academic pkgsearch.Provider
}

// New returns a Client, or an error when the config cannot work.
func New(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, fmt.Errorf("websearch: TornadURL or BaseURL is required")
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	if len(cfg.Header) > 0 {
		httpClient = withHeaders(httpClient, cfg.Header)
	}

	if strings.TrimSpace(cfg.TornadURL) != "" {
		return &Client{tornad: tornad.New(cfg.TornadURL, cfg.TornadKey, tornad.WithHTTPClient(httpClient))}, nil
	}

	provider := searxng.New(cfg.BaseURL, httpClient)
	return &Client{
		general:  categoryProvider{provider: provider, category: pkgsearch.CategoryGeneral},
		academic: categoryProvider{provider: provider, category: pkgsearch.CategoryAcademic},
	}, nil
}

// Result is the kernel's web hit.
type Result = tools.SearchResult

// Search answers one query, best first. Several categories are fused by
// reciprocal rank.
func (c *Client) Search(ctx context.Context, q tools.SearchQuery) ([]tools.SearchResult, error) {
	return c.search(ctx, q)
}

func (c *Client) search(ctx context.Context, q tools.SearchQuery) ([]tools.SearchResult, error) {
	query := strings.TrimSpace(q.Query)
	if query == "" {
		return nil, fmt.Errorf("websearch: query is empty")
	}
	maxResults := q.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	categories := categoriesFor(q.Scope)

	if c.tornad != nil {
		// tornad fuses the categories on its side, so this hands it the whole
		// list rather than merging two calls back together here.
		names := make([]string, 0, len(categories))
		for _, category := range categories {
			if category == pkgsearch.CategoryAcademic {
				names = append(names, tornad.CategoryAcademic)
				continue
			}
			names = append(names, tornad.CategoryGeneral)
		}
		req := tornad.SearchQuery{
			Query:      query,
			Categories: names,
			Limit:      maxResults,
			DeadlineMS: int(mergeDeadline / time.Millisecond),
		}
		if q.Scope == tools.ScopeRecent {
			req.TimeRange = recentRange
		}
		if q.WithContent > 0 {
			req.Content = q.WithContent
			req.ContentRunes = q.ContentRunes
			// Markdown alone: an empty format returns the text too, which is
			// the same page twice in a context that has to hold several.
			req.Format = tornad.FormatMarkdown
			// Reading pages takes far longer than ranking them, and tornad
			// bounds content on its own clock — the search deadline would cut
			// the answer before a single page came back.
			req.DeadlineMS = 0
		}
		found, err := c.tornad.Search(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("websearch: %w", err)
		}
		return fromTornad(found.Results), nil
	}

	var res *pkgsearch.Response
	var err error
	{
		providers := make([]pkgsearch.Provider, 0, len(categories))
		for _, category := range categories {
			if category == pkgsearch.CategoryAcademic {
				providers = append(providers, c.academic)
				continue
			}
			providers = append(providers, c.general)
		}
		res, err = pkgsearch.Merge(
			ctx,
			providers,
			pkgsearch.Query{Text: query, Limit: maxResults},
			mergeDeadline,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("websearch: %w", err)
	}

	out := make([]Result, 0, len(res.Results))
	for _, r := range res.Results {
		result := Result{
			Title:       r.Title,
			URL:         r.URL,
			Content:     r.Description,
			Engine:      r.Source,
			Favicon:     r.Favicon,
			SourceType:  r.SourceType,
			Author:      r.Author,
			PublishedAt: r.PublishedAt,
			Thumbnail:   r.Thumbnail,
		}
		if r.OpenGraph != nil {
			result.OGImage = r.OpenGraph.Image
			result.OGSiteName = r.OpenGraph.SiteName
		}
		out = append(out, result)
	}
	return out, nil
}

// categoryProvider pins a search.Provider's queries to one category,
// regardless of what the caller's Query.Category says — Merge above needs
// two Providers with different fixed categories, not one Provider that reads
// its category from the query.
type categoryProvider struct {
	provider pkgsearch.Provider
	category pkgsearch.Category
}

func (c categoryProvider) Search(ctx context.Context, q pkgsearch.Query) (*pkgsearch.Response, error) {
	q.Category = c.category
	return c.provider.Search(ctx, q)
}

// withHeaders wraps client so every request carries extra headers — the
// desktop sidecar authenticates its proxy calls to core this way.
func withHeaders(client *http.Client, headers http.Header) *http.Client {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone := *client
	clone.Transport = headerTransport{base: base, headers: headers}
	return &clone
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

func (t headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, vs := range t.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// fromTornad restates tornad's results in the shape the agent's tools take.
// The two vocabularies are close but not the same, and translating here is
// what keeps the kernel independent of which service answered.
func fromTornad(in []tornad.Result) []tools.SearchResult {
	out := make([]tools.SearchResult, 0, len(in))
	for _, r := range in {
		result := tools.SearchResult{
			Title:       r.Title,
			URL:         r.URL,
			Content:     r.Description,
			Engine:      r.Source,
			Favicon:     r.Favicon,
			SourceType:  r.SourceType,
			Author:      r.Author,
			PublishedAt: r.PublishedAt,
			Thumbnail:   r.Thumbnail,
			// Filled only when the search asked tornad to read the pages.
			Text:         r.Text,
			Markdown:     r.Markdown,
			ContentError: r.ContentError,
		}
		if r.OpenGraph != nil {
			result.OGImage = r.OpenGraph.Image
			result.OGSiteName = r.OpenGraph.SiteName
		}
		out = append(out, result)
	}
	return out
}

// categoriesFor maps what the agent asked for onto what this backend sorts
// results into. The kernel names an intent — studies, recency, the open web —
// and never a category: which engines answer a scope is this implementation's
// business, and tornad's alone to rename.
func categoriesFor(scope tools.SearchScope) []pkgsearch.Category {
	switch scope {
	case tools.ScopeStudies:
		return []pkgsearch.Category{pkgsearch.CategoryAcademic}
	case tools.ScopeLocal, tools.ScopeRecent:
		// Recency is a filter on the general category, not a category of its
		// own: scholarly publishing runs in months, so mixing it into a
		// question about the last few weeks only adds noise.
		return []pkgsearch.Category{pkgsearch.CategoryGeneral}
	default:
		return []pkgsearch.Category{pkgsearch.CategoryGeneral, pkgsearch.CategoryAcademic}
	}
}
