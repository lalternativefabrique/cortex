// Package sitemap lists what a site holds, through tornad's /map.
//
// It reads structure, not content: one walk of a site's links, where reading
// the pages would be one render each. That is what makes it affordable to
// offer an agent looking for the right page rather than the right site.
package sitemap

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lalternative/packages/go/cortex/tools"

	tornad "github.com/lalternative/packages/tornad/sdk-go"
)

// Config points a Client at a tornad.
type Config struct {
	// BaseURL points at a tornad instance. Empty leaves explore_site out:
	// there is no local fallback for walking a site, unlike reading one page.
	BaseURL string
	// Key authenticates server-to-server calls. Empty sends nothing, which an
	// internal-only tornad accepts.
	Key        string
	HTTPClient *http.Client
}

// Configured reports whether there is a backend to explore with.
func (c Config) Configured() bool { return strings.TrimSpace(c.BaseURL) != "" }

// Client walks sites.
type Client struct {
	tornad *tornad.Client
}

// New returns a Client, or nil when nothing is configured.
func New(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	opts := []tornad.Option{}
	if cfg.HTTPClient != nil {
		opts = append(opts, tornad.WithHTTPClient(cfg.HTTPClient))
	}
	return &Client{tornad: tornad.New(cfg.BaseURL, cfg.Key, opts...)}
}

// Explore returns the URLs reachable from req.URL, within its scope.
func (c *Client) Explore(ctx context.Context, req tools.ExploreRequest) ([]string, error) {
	found, err := c.tornad.Map(ctx, tornad.MapRequest{
		Scope: tornad.Scope{
			URL:          req.URL,
			MaxPages:     req.MaxPages,
			IncludePaths: req.Include,
			ExcludePaths: req.Exclude,
		},
		Limit: req.MaxPages,
	})
	if err != nil {
		return nil, fmt.Errorf("sitemap: %w", err)
	}
	return found.Links, nil
}
