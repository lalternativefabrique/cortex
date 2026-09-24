package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type stubExplorer struct {
	got   ExploreRequest
	links []string
	err   error
}

func (s *stubExplorer) Explore(_ context.Context, req ExploreRequest) ([]string, error) {
	s.got = req
	return s.links, s.err
}

func TestExploreListsPages(t *testing.T) {
	explorer := &stubExplorer{links: []string{
		"https://example.test/docs/a",
		"https://example.test/docs/b",
	}}
	tool := NewExploreSite(ExploreConfig{Explorer: explorer})

	res := runToolOK(t, tool, `{"url":"https://example.test/docs"}`)
	for _, want := range explorer.links {
		if !strings.Contains(res.Content, want) {
			t.Errorf("content is %q, want it to list %s", res.Content, want)
		}
	}
	if res.Metadata["pages"] != 2 {
		t.Errorf("pages is %v, want 2", res.Metadata["pages"])
	}
}

// Narrowing is what keeps a walk on the part of a site that was asked about,
// so the filters the model gives have to reach the backend.
func TestExploreCarriesFilters(t *testing.T) {
	explorer := &stubExplorer{links: []string{"https://example.test/docs/a"}}
	tool := NewExploreSite(ExploreConfig{Explorer: explorer})

	runToolOK(t, tool, `{"url":"https://example.test","include":["/docs/"],"exclude":["/blog/"]}`)
	if len(explorer.got.Include) != 1 || explorer.got.Include[0] != "/docs/" {
		t.Errorf("include is %v, want [/docs/]", explorer.got.Include)
	}
	if len(explorer.got.Exclude) != 1 || explorer.got.Exclude[0] != "/blog/" {
		t.Errorf("exclude is %v, want [/blog/]", explorer.got.Exclude)
	}
	if explorer.got.MaxPages <= 0 {
		t.Error("MaxPages is unset — an unbounded site makes an unbounded call")
	}
}

// An answer founded on a partial map is founded on a partial site, so a cut
// list has to say it was cut.
func TestExploreSaysWhenTruncated(t *testing.T) {
	links := make([]string, exploreListed+25)
	for i := range links {
		links[i] = fmt.Sprintf("https://example.test/p/%d", i)
	}
	tool := NewExploreSite(ExploreConfig{Explorer: &stubExplorer{links: links}})

	res := runToolOK(t, tool, `{"url":"https://example.test"}`)
	if res.Metadata["truncated"] != true {
		t.Errorf("truncated is %v, want true", res.Metadata["truncated"])
	}
	if !strings.Contains(res.Content, "not shown") {
		t.Errorf("content is %q, want it to say the list was cut", res.Content)
	}
	if strings.Contains(res.Content, "/p/125") {
		t.Error("a link past the cap was listed")
	}
}

func TestExploreWithNothingFound(t *testing.T) {
	tool := NewExploreSite(ExploreConfig{Explorer: &stubExplorer{}})

	res := runToolOK(t, tool, `{"url":"https://example.test"}`)
	if !strings.Contains(res.Content, "No pages") {
		t.Errorf("content is %q, want it to report an empty walk", res.Content)
	}
}

func TestExploreNeedsAURL(t *testing.T) {
	tool := NewExploreSite(ExploreConfig{Explorer: &stubExplorer{}})

	res := runToolOK(t, tool, `{"url":"  "}`)
	if res.Metadata["ok"] != false {
		t.Errorf("an empty url was accepted: %+v", res)
	}
}
