package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

const (
	DrawToolName       = "draw"
	FindImagesToolName = "find_images"
)

// NewDrawTool lets the model draw a figure, as SVG, attached to the reply.
// The drawing is cleaned (CleanSVG) before it is attached.
func NewDrawTool() agent.Tool { return drawTool{} }

type drawInput struct {
	Title string `json:"title" jsonschema:"description=What the drawing shows, in a few words, in the person's language. It is the picture's caption and alt text."`
	SVG   string `json:"svg" jsonschema:"description=The whole drawing as one SVG document: a root <svg> with a viewBox, then shapes, paths, text, gradients. No script, no external reference, no embedded image."`
}

type drawTool struct{}

func (drawTool) Name() string { return DrawToolName }
func (drawTool) Description() string {
	return strings.Join([]string{
		"Draw something and show it in the answer.",
		"",
		"You draw freely, as SVG: an explanatory sketch, a plan, a map, a timeline, a chart of real figures, a comparison, an icon, a scene. Use it whenever a drawing says it better than words, and when the person asks you to draw.",
		"",
		"Write the drawing in full: `<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 W H\">…</svg>`, width 800 at most, every text in a readable size (14 or more at that scale), sober colours. Scripts, external links, embedded images and foreign content are dropped.",
		"",
		"A drawing is a sketch: keep it under 4000 characters and about twenty elements, with flat fills. Write it directly, without drafting it first.",
		"",
		"Returns one markdown image line, `![title](attachment://id)`. Copy it verbatim into the answer where the drawing belongs; never write an image reference the tool did not return.",
	}, "\n")
}
func (drawTool) InputSchema() any { return drawInput{} }

func (drawTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in drawInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return toolFailure("could not parse arguments: %v", err), nil
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return toolFailure("`title` is required"), nil
	}
	if strings.TrimSpace(in.SVG) == "" {
		return toolFailure("`svg` is required"), nil
	}
	clean, err := CleanSVG([]byte(in.SVG))
	switch {
	case errors.Is(err, ErrDrawingTooBig):
		return toolFailure("the drawing is too large (%d bytes max); simplify it", MaxDrawingBytes), nil
	case errors.Is(err, ErrNotSVG):
		return toolFailure("`svg` is not a well-formed SVG document: %v", err), nil
	case err != nil:
		return toolFailure("could not read the drawing: %v", err), nil
	}
	ref, err := Attach(ctx, Attachment{Name: in.Title + ".svg", MimeType: "image/svg+xml", Bytes: clean})
	if err != nil {
		return toolFailure("could not attach the drawing: %v", err), nil
	}
	return agent.ToolResult{
		Content:  fmt.Sprintf("![%s](%s)", imageAlt(in.Title), ref),
		Metadata: map[string]any{"ok": true, "title": in.Title, "attachments": []string{ref}},
	}, nil
}

// ImageHit is one picture an image search found.
type ImageHit struct {
	Title    string
	PageURL  string
	ImageURL string
}

// ImageSearcher asks a search backend for pictures.
type ImageSearcher interface {
	SearchImages(ctx context.Context, query string, limit int) ([]ImageHit, error)
}

const (
	defaultImageCount = 2
	maxImageCount     = 4
)

// NewFindImagesTool lets the model show pictures found on the web: each is
// attached to the reply by its URL, which the caller fetches.
func NewFindImagesTool(search ImageSearcher) agent.Tool { return findImagesTool{search: search} }

type findImagesInput struct {
	Query string `json:"query" jsonschema:"description=What the pictures should show, as a short visual search: the subject and what makes it recognisable."`
	Count int    `json:"count,omitempty" jsonschema:"description=How many pictures to bring back, 1 to 4. Default 2."`
}

type findImagesTool struct{ search ImageSearcher }

func (findImagesTool) Name() string { return FindImagesToolName }
func (findImagesTool) Description() string {
	return strings.Join([]string{
		"Find pictures on the web to show in the answer.",
		"",
		"Use it when seeing is part of answering: what something looks like, a place, a work, a plant or an animal, a visual comparison. One call per subject.",
		"",
		"Returns one markdown image line per picture, `![title](attachment://id)`, each credited with the page it comes from. Copy those lines verbatim into the answer where the picture belongs; never write an image reference the tool did not return.",
	}, "\n")
}
func (findImagesTool) InputSchema() any { return findImagesInput{} }

func (t findImagesTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var in findImagesInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return toolFailure("could not parse arguments: %v", err), nil
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return toolFailure("`query` is required"), nil
	}
	count := min(max(in.Count, 0), maxImageCount)
	if count == 0 {
		count = defaultImageCount
	}
	hits, err := t.search.SearchImages(ctx, in.Query, count)
	if err != nil {
		return toolFailure("could not search for pictures: %v", err), nil
	}
	var b strings.Builder
	refs := []string{}
	for _, h := range hits {
		if len(refs) == count {
			break
		}
		if strings.TrimSpace(h.ImageURL) == "" {
			continue
		}
		ref, err := Attach(ctx, Attachment{Name: h.Title, URI: h.ImageURL})
		if err != nil {
			return toolFailure("could not attach the picture: %v", err), nil
		}
		refs = append(refs, ref)
		fmt.Fprintf(&b, "![%s](%s)\nsource: %s\n\n", imageAlt(h.Title), ref, h.PageURL)
	}
	if len(refs) == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("no usable picture found for %q; answer without one", in.Query),
			Metadata: map[string]any{"ok": true, "query": in.Query, "attachments": refs},
		}, nil
	}
	return agent.ToolResult{
		Content:  strings.TrimSpace(b.String()),
		Metadata: map[string]any{"ok": true, "query": in.Query, "attachments": refs},
	}, nil
}

// imageAlt keeps a title usable inside `![...]`.
func imageAlt(title string) string {
	r := strings.NewReplacer("[", "(", "]", ")", "\n", " ", "\r", " ")
	alt := strings.TrimSpace(r.Replace(title))
	if alt == "" {
		return "image"
	}
	return alt
}
