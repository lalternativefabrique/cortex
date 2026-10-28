package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/tools"
)

const (
	readAttachmentName  = "read_attachment"
	maxAttachmentBytes  = 20 << 20
	maxAttachedText     = 60_000
	documentAnswerLimit = 8000
)

// TurnFile is a file the person attached to a turn, base64-encoded.
type TurnFile struct {
	Name     string `json:"name"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

// Attachment is a decoded file of the conversation.
type Attachment struct {
	Name     string
	MimeType string
	Bytes    []byte
}

// DocumentReader reads a document such as a PDF; vision.Describer is one.
type DocumentReader interface {
	ReadDocument(ctx context.Context, data []byte, mime, question string, maxTokens int) (string, error)
}

type attachmentsKey struct{}

// Attachments are the files attached so far in the conversation the turn
// running under ctx belongs to, for a host tool that files them somewhere.
func Attachments(ctx context.Context) []Attachment {
	files, _ := ctx.Value(attachmentsKey{}).([]Attachment)
	return files
}

func withAttachments(ctx context.Context, files []Attachment) context.Context {
	if len(files) == 0 {
		return ctx
	}
	return context.WithValue(ctx, attachmentsKey{}, files)
}

func decodeTurnFiles(in []TurnFile) ([]Attachment, error) {
	out := make([]Attachment, 0, len(in))
	for i, f := range in {
		raw, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			return nil, fmt.Errorf("file %q is not base64", f.Name)
		}
		if len(raw) > maxAttachmentBytes {
			return nil, fmt.Errorf("file %q is over %d MB", f.Name, maxAttachmentBytes>>20)
		}
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = fmt.Sprintf("file-%d", i+1)
		}
		out = append(out, Attachment{Name: name, MimeType: strings.TrimSpace(f.MimeType), Bytes: raw})
	}
	return out, nil
}

func attachedNote(files []Attachment) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Files attached to this message (read them with " + readAttachmentName + "):")
	for _, f := range files {
		fmt.Fprintf(&b, "\n- %s (%s, %d KB)", f.Name, f.MimeType, (len(f.Bytes)+1023)/1024)
	}
	return b.String()
}

func mergeAttachments(held, added []Attachment) []Attachment {
	merged := append([]Attachment(nil), held...)
	for _, a := range added {
		replaced := false
		for i := range merged {
			if merged[i].Name == a.Name {
				merged[i], replaced = a, true
			}
		}
		if !replaced {
			merged = append(merged, a)
		}
	}
	return merged
}

type readAttachment struct {
	describer tools.BytesDescriber
	documents DocumentReader
	files     []Attachment
}

type readAttachmentArgs struct {
	File     string `json:"file" jsonschema:"description=The name of a file attached to the conversation."`
	Question string `json:"question,omitempty" jsonschema:"description=What you need from the file. A specific question yields a useful answer."`
}

func newReadAttachment(d tools.BytesDescriber, files []Attachment) agent.Tool {
	if len(files) == 0 {
		return nil
	}
	t := &readAttachment{describer: d, files: files}
	if r, ok := d.(DocumentReader); ok {
		t.documents = r
	}
	return t
}

func (t *readAttachment) Name() string { return readAttachmentName }
func (t *readAttachment) Description() string {
	return "Read a file attached to the conversation: an image is described, a PDF is transcribed, a text file is returned as is. Ask a specific question for an image or a long document."
}
func (t *readAttachment) InputSchema() any { return readAttachmentArgs{} }

func (t *readAttachment) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args readAttachmentArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return attachmentFailure("could not parse arguments: %v", err), nil
	}
	var names []string
	for _, f := range t.files {
		names = append(names, f.Name)
		if f.Name != args.File {
			continue
		}
		text, err := t.read(ctx, f, args.Question)
		if err != nil {
			return attachmentFailure("%v", err), nil
		}
		return agent.ToolResult{Content: text, Metadata: map[string]any{"ok": true, "file": f.Name}}, nil
	}
	return attachmentFailure("no file %q is attached; attached: %s", args.File, strings.Join(names, ", ")), nil
}

func (t *readAttachment) read(ctx context.Context, f Attachment, question string) (string, error) {
	switch {
	case strings.HasPrefix(f.MimeType, "image/"):
		if t.describer == nil {
			return "", fmt.Errorf("this agent has no vision model: images cannot be read")
		}
		return t.describer.DescribeBytes(ctx, f.Bytes, f.MimeType, question)
	case f.MimeType == "application/pdf":
		if t.documents == nil {
			return "", fmt.Errorf("this agent has no vision model: PDFs cannot be read")
		}
		if strings.TrimSpace(question) == "" {
			question = "Transcribe this document faithfully, keeping its structure."
		}
		return t.documents.ReadDocument(ctx, f.Bytes, f.MimeType, question, documentAnswerLimit)
	case utf8.Valid(f.Bytes):
		text := string(f.Bytes)
		if len(text) > maxAttachedText {
			text = text[:maxAttachedText] + "\n[truncated]"
		}
		return text, nil
	}
	return "", fmt.Errorf("%s files cannot be read", f.MimeType)
}

func attachmentFailure(format string, args ...any) agent.ToolResult {
	return agent.ToolResult{Content: "error: " + fmt.Sprintf(format, args...), Metadata: map[string]any{"ok": false}}
}

func (s *Server) withReadAttachment(base []agent.Tool, files []Attachment) []agent.Tool {
	bytes, _ := s.describer.(tools.BytesDescriber)
	t := newReadAttachment(bytes, files)
	if t == nil {
		return base
	}
	return append(slices.Clone(base), t)
}
