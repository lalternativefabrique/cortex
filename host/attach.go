package host

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
)

// AttachmentScheme prefixes the reference to an attachment in the answer:
// `![title](attachment://<id>)`, where id is the attachment artifact's id.
const AttachmentScheme = "attachment://"

// Attachment is a file a tool adds to the reply: its bytes, or a URI the
// caller fetches.
type Attachment struct {
	Name     string
	MimeType string
	Bytes    []byte
	URI      string
}

// ErrNoTurn is returned by Attach outside a turn the host runs.
var ErrNoTurn = errors.New("host: no turn to attach to")

type attacherKey struct{}

type attacher func(ctx context.Context, a Attachment) (string, error)

// Attach adds a to the reply of the turn running under ctx, as one
// "attachment" artifact holding one file part, and returns the reference the
// answer shows it by.
func Attach(ctx context.Context, a Attachment) (string, error) {
	attach, _ := ctx.Value(attacherKey{}).(attacher)
	if attach == nil {
		return "", ErrNoTurn
	}
	if len(a.Bytes) == 0 && strings.TrimSpace(a.URI) == "" {
		return "", errors.New("host: an attachment needs bytes or a URI")
	}
	return attach(ctx, a)
}

func withAttacher(ctx context.Context, s *stream) context.Context {
	return context.WithValue(ctx, attacherKey{}, attacher(s.attach))
}

func (s *stream) attach(_ context.Context, a Attachment) (string, error) {
	meta := a2a.FileMeta{Name: a.Name, MimeType: a.MimeType}
	var file a2a.FilePartContent = a2a.FileURI{FileMeta: meta, URI: a.URI}
	if len(a.Bytes) > 0 {
		file = a2a.FileBytes{FileMeta: meta, Bytes: base64.StdEncoding.EncodeToString(a.Bytes)}
	}
	ev := a2a.NewArtifactEvent(s.task, a2a.FilePart{File: file})
	ev.Artifact.Name = "attachment"
	ev.Artifact.Description = a.Name
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.emit(s.ctx, ev); err != nil {
		return "", err
	}
	return AttachmentScheme + string(ev.Artifact.ID), nil
}
