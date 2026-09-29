package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/connectors/web/fetchpage"
	"github.com/lalternative/packages/go/cortex/connectors/web/websearch"
	"github.com/lalternative/packages/go/cortex/tools"
)

// webToolsFromEnv gives the agent web_search and fetch_url, served by the
// tornad at CORTEX_TORNAD_URL. Unset, the agent reads no web of its own:
// only what its MCP servers offer.
func webToolsFromEnv() ([]agent.Tool, error) {
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("CORTEX_TORNAD_URL")), "/")
	if url == "" {
		return nil, nil
	}
	key := strings.TrimSpace(os.Getenv("CORTEX_TORNAD_KEY"))
	searcher, err := websearch.New(websearch.Config{TornadURL: url, TornadKey: key})
	if err != nil {
		return nil, fmt.Errorf("CORTEX_TORNAD_URL: %w", err)
	}
	return []agent.Tool{
		tools.NewWebSearch(tools.WebSearchConfig{Searcher: searcher}),
		tools.NewFetchURL(tools.FetchURLConfig{Fetcher: fetchpage.New(fetchpage.Config{BaseURL: url, Key: key})}),
	}, nil
}
