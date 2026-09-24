package tools

import (
	"context"
	"strings"
	"testing"
)

type stubFetcher struct {
	got  FetchRequest
	page *Page
}

func (s *stubFetcher) Fetch(_ context.Context, req FetchRequest) (*Page, error) {
	s.got = req
	if s.page != nil {
		return s.page, nil
	}
	return &Page{}, nil
}

func TestFetchURLDoesNotRenderByDefault(t *testing.T) {
	fetcher := &stubFetcher{page: &Page{Title: "t", Text: "body"}}
	tool := NewFetchURL(FetchURLConfig{Fetcher: fetcher})

	runToolOK(t, tool, `{"url":"https://example.test/a"}`)
	if fetcher.got.Render {
		t.Error("a plain read rendered the page — that costs seconds and an engine for most pages")
	}
}

func TestFetchURLRendersWhenAsked(t *testing.T) {
	fetcher := &stubFetcher{page: &Page{Title: "t", Text: "body"}}
	tool := NewFetchURL(FetchURLConfig{Fetcher: fetcher})

	runToolOK(t, tool, `{"url":"https://example.test/a","render":true}`)
	if !fetcher.got.Render {
		t.Error("render was asked for and not passed on")
	}
}

// An empty read is where the model has to learn that render exists: without
// the hint it simply reports the page as empty and moves on.
func TestFetchURLEmptyPageSuggestsRender(t *testing.T) {
	tool := NewFetchURL(FetchURLConfig{Fetcher: &stubFetcher{page: &Page{}}})

	res := runToolOK(t, tool, `{"url":"https://example.test/app"}`)
	if !strings.Contains(res.Content, "render") {
		t.Errorf("content is %q, want it to point at render", res.Content)
	}
}

// Once rendered, an empty page is empty: suggesting render again would send
// the model round a loop it cannot win.
func TestFetchURLEmptyAfterRenderSaysSo(t *testing.T) {
	tool := NewFetchURL(FetchURLConfig{Fetcher: &stubFetcher{page: &Page{}}})

	res := runToolOK(t, tool, `{"url":"https://example.test/app","render":true}`)
	if strings.Contains(res.Content, "call this again") {
		t.Errorf("content is %q, want it not to suggest rendering twice", res.Content)
	}
	if res.Metadata["rendered"] != true {
		t.Errorf("rendered is %v, want true", res.Metadata["rendered"])
	}
}
