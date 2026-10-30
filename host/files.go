package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/tools"
)

// File is a file part of the message a turn answers: its bytes when the
// caller sent them, else the URI it named.
type File struct {
	Name     string
	MimeType string
	Bytes    []byte
	URI      string
}

func (f File) isImage() bool {
	return strings.HasPrefix(f.MimeType, "image/") && len(f.Bytes) > 0
}

type turnFilesKey struct{}

// TurnFiles are the files of the message the turn running under ctx answers.
func TurnFiles(ctx context.Context) []File {
	files, _ := ctx.Value(turnFilesKey{}).([]File)
	return files
}

func withTurnFiles(ctx context.Context, files []File) context.Context {
	if len(files) == 0 {
		return ctx
	}
	return context.WithValue(ctx, turnFilesKey{}, files)
}

func messageFiles(msg *a2a.Message) ([]File, error) {
	if msg == nil {
		return nil, nil
	}
	var files []File
	for _, p := range msg.Parts {
		part, ok := p.(a2a.FilePart)
		if !ok {
			continue
		}
		f := File{}
		switch c := part.File.(type) {
		case a2a.FileBytes:
			raw, err := base64.StdEncoding.DecodeString(c.Bytes)
			if err != nil {
				return nil, fmt.Errorf("file %q is not base64: %w", c.Name, err)
			}
			f = File{Name: c.Name, MimeType: c.MimeType, Bytes: raw}
		case a2a.FileURI:
			f = File{Name: c.Name, MimeType: c.MimeType, URI: c.URI}
		default:
			continue
		}
		if strings.TrimSpace(f.Name) == "" {
			f.Name = fmt.Sprintf("file-%d", len(files)+1)
		}
		files = append(files, f)
	}
	return files, nil
}

// filesNote tells the model what came with the message, so it can name a
// file to the tool that reads it.
func filesNote(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Files attached to this message:")
	for _, f := range files {
		fmt.Fprintf(&b, "\n- %s (%s)", f.Name, f.MimeType)
		if f.URI != "" {
			fmt.Fprintf(&b, " at %s", f.URI)
		}
	}
	return b.String()
}

func withFilesNote(asked string, files []File) string {
	note := filesNote(files)
	if note == "" {
		return asked
	}
	return strings.TrimSpace(asked + "\n\n" + note)
}

const describeAttachedImageName = "describe_image"

type describeAttachedImage struct {
	describer tools.BytesDescriber
	files     []File
}

type describeAttachedImageArgs struct {
	File     string `json:"file" jsonschema:"description=The name of an image attached to the message."`
	Question string `json:"question,omitempty" jsonschema:"description=What you need to know about the image. A specific question yields a useful answer where a generic one yields a generic caption."`
}

func newDescribeAttachedImage(d tools.BytesDescriber, files []File) agent.Tool {
	if d == nil {
		return nil
	}
	var images []File
	for _, f := range files {
		if f.isImage() {
			images = append(images, f)
		}
	}
	if len(images) == 0 {
		return nil
	}
	return &describeAttachedImage{describer: d, files: images}
}

func (t *describeAttachedImage) Name() string { return describeAttachedImageName }
func (t *describeAttachedImage) Description() string {
	return strings.Join([]string{
		"Look at an image attached to the message and get back a description in text.",
		"",
		"Ask a specific question. Text in the image is transcribed verbatim. The description is a reading by another model: treat an odd detail as possibly misread.",
	}, "\n")
}
func (t *describeAttachedImage) InputSchema() any { return describeAttachedImageArgs{} }

func (t *describeAttachedImage) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args describeAttachedImageArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolFailure("could not parse arguments: %v", err), nil
	}
	for _, f := range t.files {
		if f.Name != args.File {
			continue
		}
		description, err := t.describer.DescribeBytes(ctx, f.Bytes, f.MimeType, args.Question)
		if err != nil {
			return toolFailure("%v", err), nil
		}
		return agent.ToolResult{Content: description, Metadata: map[string]any{"ok": true, "file": f.Name, "model": t.describer.Model()}}, nil
	}
	names := make([]string, 0, len(t.files))
	for _, f := range t.files {
		names = append(names, f.Name)
	}
	return toolFailure("no image %q is attached; attached: %s", args.File, strings.Join(names, ", ")), nil
}

func toolFailure(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + fmt.Sprintf(format, args...), Metadata: map[string]any{"ok": false}}
}
