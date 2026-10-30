package tools

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/lalternativefabrique/cortex/agent"
)

// WebPage is one web page as web_search and fetch_url hand it to the model
// and to the caller: Ref is what the answer cites, as [W1].
type WebPage struct {
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Excerpt string `json:"excerpt"`
}

const citeWebPages = "Cite each page you use by its ref in brackets, copied as is: [W1]."

func webPagesContent(ctx context.Context, pages []WebPage) string {
	for i := range pages {
		ref, ok := agent.WebRef(ctx, pages[i].URL)
		if !ok {
			ref = "W" + strconv.Itoa(i+1)
		}
		pages[i].Ref = ref
	}
	out, _ := json.Marshal(pages)
	return string(out)
}
