package serve

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

type eyes struct{ stubDescriber }

func (eyes) DescribeBytes(_ context.Context, data []byte, mime, _ string) (string, error) {
	return "a picture of " + mime, nil
}

func (eyes) ReadDocument(_ context.Context, data []byte, mime, _ string, _ int) (string, error) {
	return "transcribed " + string(data), nil
}

func TestAttachedFilesAreReadByTheirKind(t *testing.T) {
	files, err := decodeTurnFiles([]TurnFile{
		{Name: "vitrine.jpg", MimeType: "image/jpeg", Data: base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8})},
		{Name: "facture.pdf", MimeType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte("%PDF"))},
		{Name: "notes.txt", MimeType: "text/plain", Data: base64.StdEncoding.EncodeToString([]byte("acheter du pain"))},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newReadAttachment(eyes{}, files)
	for file, want := range map[string]string{
		"vitrine.jpg": "a picture of image/jpeg",
		"facture.pdf": "transcribed %PDF",
		"notes.txt":   "acheter du pain",
		"absent.png":  "no file \"absent.png\" is attached",
	} {
		res, err := tool.Execute(context.Background(), json.RawMessage(`{"file":"`+file+`"}`))
		if err != nil || !strings.Contains(res.Content, want) {
			t.Errorf("%s: got %q, %v", file, res.Content, err)
		}
	}
	if !strings.Contains(attachedNote(files), "facture.pdf (application/pdf") {
		t.Errorf("note %q", attachedNote(files))
	}
}

func TestWithoutVisionAnImageSaysWhyItCannotBeRead(t *testing.T) {
	files, _ := decodeTurnFiles([]TurnFile{{Name: "a.png", MimeType: "image/png", Data: "AA=="}})
	res, _ := newReadAttachment(nil, files).Execute(context.Background(), json.RawMessage(`{"file":"a.png"}`))
	if !strings.Contains(res.Content, "no vision model") {
		t.Errorf("got %q", res.Content)
	}
}

func TestAFileAttachedAgainReplacesTheOldOne(t *testing.T) {
	got := mergeAttachments([]Attachment{{Name: "a", Bytes: []byte("1")}}, []Attachment{{Name: "a", Bytes: []byte("2")}, {Name: "b"}})
	if len(got) != 2 || string(got[0].Bytes) != "2" {
		t.Errorf("got %+v", got)
	}
}

func TestANonBase64FileIsRefused(t *testing.T) {
	if _, err := decodeTurnFiles([]TurnFile{{Name: "x", Data: "%%%"}}); err == nil {
		t.Error("accepted")
	}
}
