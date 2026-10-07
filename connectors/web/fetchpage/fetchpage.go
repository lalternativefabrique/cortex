// Package fetchpage reads a web page's main text, for reading a page a
// web_search result pointed at.
//
// It prefers vvaves's POST /fetch (github.com/lalternativefabrique/vvaves),
// which owns readability, the JavaScript fallback, the residential proxy and
// challenge detection in one call. Without a vvaves it extracts locally
// through github.com/lalternative/packages/go/search/fetch, which is the same
// readability pass minus everything vvaves adds around it.
package fetchpage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/tools"

	"github.com/lalternative/packages/go/search/fetch"
	tornad "github.com/lalternative/packages/tornad/sdk-go"
)

const (
	// DefaultMaxRunes caps what one page contributes to the model's context.
	// A long article otherwise crowds out every other source gathered this
	// turn.
	DefaultMaxRunes = 6000
	// cacheTTL bounds how long a locally fetched page is reused across calls
	// in the same run — long enough that re-reading the same URL a few turns
	// later skips the network round trip, short enough that a long-running
	// process does not serve stale content indefinitely. Vvaves keeps its own
	// cache, so this covers the local path only.
	cacheTTL = 15 * time.Minute
	// fetchTimeout bounds a call to tornad. Its own fetch renders a
	// JavaScript page in-process, so the wait covers a render too.
	fetchTimeout = 45 * time.Second
)

var errNoReader = errors.New("fetchpage: no reader configured and local extraction is disabled")

// Page is the kernel's page.
type Page = tools.Page

// Config points a Client at a vvaves.
type Config struct {
	// BaseURL points at a tornad instance. Empty extracts locally instead.
	BaseURL string
	// Key authenticates server-to-server calls on a tornad reachable from the
	// internet. Empty sends nothing, which an internal-only tornad accepts.
	Key string
	// HTTPClient reaches tornad and defaults to one bounded by fetchTimeout.
	// The local extraction does not use it: it runs on go/search's guarded
	// client, which refuses internal addresses on every hop.
	HTTPClient *http.Client
	// NoLocalFallback refuses to read a page here when tornad is not
	// configured or the site refused it, rather than extracting it locally.
	NoLocalFallback bool
}

// Client reads pages, through vvaves when one is configured.
type Client struct {
	cfg Config
	// tornad is nil when no BaseURL was configured, which leaves every read
	// on local static extraction.
	tornad *tornad.Client
	cache  fetch.Cache
}

// New returns a Client reading through the tornad at cfg.BaseURL. Empty
// BaseURL falls back to local extraction, which has no JavaScript rendering:
// a client-side-rendered page then comes back empty.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: fetchTimeout}
	}
	c := &Client{cfg: cfg, cache: fetch.NewMemoryCache(cacheTTL)}
	if cfg.BaseURL != "" {
		c.tornad = tornad.New(cfg.BaseURL, cfg.Key, tornad.WithHTTPClient(cfg.HTTPClient))
	}
	return c
}

// Fetch reads a page's main text.
func (c *Client) Fetch(ctx context.Context, req tools.FetchRequest) (*tools.Page, error) {
	url := req.URL
	maxRunes := req.MaxRunes
	if maxRunes <= 0 {
		maxRunes = DefaultMaxRunes
	}
	var refused error
	if c.tornad != nil {
		page, err := c.fetchRemote(ctx, url, maxRunes, req.Render)
		// ErrUpstream is the page refusing to be read, not tornad failing:
		// a publisher blocking a datacenter address, a page that never
		// settles. Retrying it is pointless, but a static read from here may
		// still succeed where a rendered one did not, so it falls through.
		if err == nil || !errors.Is(err, tornad.ErrUpstream) {
			return page, err
		}
		refused = err
	}
	if c.cfg.NoLocalFallback {
		if refused != nil {
			return nil, fmt.Errorf("the site refused to serve this page through the reader: %w", refused)
		}
		return nil, errNoReader
	}
	page, err := fetch.FetchStatic(ctx, url, maxRunes, c.cache)
	if err != nil {
		if refused != nil {
			return nil, fmt.Errorf("the site refused to serve this page, through the reader and directly (%v; direct read: %w)", refused, err)
		}
		return nil, err
	}
	return &Page{Title: page.Title, Text: page.Text}, nil
}

func (c *Client) fetchRemote(ctx context.Context, url string, maxRunes int, render bool) (*tools.Page, error) {
	page, err := c.tornad.Fetch(ctx, tornad.FetchRequest{
		URL:      url,
		MaxRunes: maxRunes,
		Render:   &render,
		Format:   tornad.FormatText,
	})
	if err != nil {
		return nil, fmt.Errorf("fetchpage: %w", err)
	}
	return &Page{Title: page.Title, Text: page.Text}, nil
}
