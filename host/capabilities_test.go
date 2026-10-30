package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/tools"
)

func recordingStream() (*stream, *[]a2a.Event) {
	var mu sync.Mutex
	var events []a2a.Event
	s := &stream{ctx: context.Background(), task: Turn{TaskID: "t1", ContextID: "c1"}, emit: func(_ context.Context, ev a2a.Event) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
		return nil
	}}
	return s, &events
}

func TestAttachOutsideATurnIsRefused(t *testing.T) {
	if _, err := Attach(context.Background(), Attachment{Name: "a", Bytes: []byte("x")}); !errors.Is(err, ErrNoTurn) {
		t.Fatalf("err = %v", err)
	}
}

func TestDrawAttachesTheCleanedDrawingToTheReply(t *testing.T) {
	s, events := recordingStream()
	ctx := withAttacher(context.Background(), s)
	args, _ := json.Marshal(map[string]string{"title": "Plan", "svg": `<svg viewBox="0 0 10 10"><script>x</script><rect width="5" height="5"/></svg>`})

	res, err := NewDrawTool().Execute(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %d", len(*events))
	}
	ev := (*events)[0].(*a2a.TaskArtifactUpdateEvent)
	if ev.Artifact.Name != "attachment" || res.Content != "![Plan]("+AttachmentScheme+string(ev.Artifact.ID)+")" {
		t.Fatalf("artifact %q, content %q", ev.Artifact.Name, res.Content)
	}
	file := ev.Artifact.Parts[0].(a2a.FilePart).File.(a2a.FileBytes)
	svg, _ := base64.StdEncoding.DecodeString(file.Bytes)
	if file.MimeType != "image/svg+xml" || strings.Contains(string(svg), "script") || !strings.Contains(string(svg), "<rect") {
		t.Errorf("attached %s %s", file.MimeType, svg)
	}
}

type images []ImageHit

func (i images) SearchImages(context.Context, string, int) ([]ImageHit, error) { return i, nil }

func TestFindImagesAttachesEachPictureByItsURL(t *testing.T) {
	s, events := recordingStream()
	ctx := withAttacher(context.Background(), s)
	tool := NewFindImagesTool(images{{Title: "Mont [1]", PageURL: "https://p", ImageURL: "https://i/1.jpg"}, {Title: "no image"}})

	res, err := tool.Execute(ctx, json.RawMessage(`{"query":"mont"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %d", len(*events))
	}
	file := (*events)[0].(*a2a.TaskArtifactUpdateEvent).Artifact.Parts[0].(a2a.FilePart).File.(a2a.FileURI)
	if file.URI != "https://i/1.jpg" || !strings.HasPrefix(res.Content, "![Mont (1)]("+AttachmentScheme) || !strings.Contains(res.Content, "source: https://p") {
		t.Errorf("uri %q, content %q", file.URI, res.Content)
	}
}

type describer struct{ got []byte }

func (d *describer) Describe(context.Context, string, string) (string, error) { return "", nil }
func (d *describer) Model() string                                            { return "v" }
func (d *describer) DescribeBytes(_ context.Context, data []byte, _, _ string) (string, error) {
	d.got = data
	return "a cat", nil
}

func TestTheMessagesImagesAreReadByDescribeImage(t *testing.T) {
	msg := &a2a.Message{Parts: a2a.ContentParts{
		a2a.TextPart{Text: "what is it?"},
		a2a.FilePart{File: a2a.FileBytes{FileMeta: a2a.FileMeta{Name: "cat.png", MimeType: "image/png"}, Bytes: base64.StdEncoding.EncodeToString([]byte("png"))}},
		a2a.FilePart{File: a2a.FileURI{FileMeta: a2a.FileMeta{MimeType: "application/pdf"}, URI: "https://f/doc.pdf"}},
	}}
	files, err := messageFiles(msg)
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	if note := withFilesNote("what is it?", files); !strings.Contains(note, "cat.png (image/png)") || !strings.Contains(note, "at https://f/doc.pdf") {
		t.Errorf("note = %q", note)
	}
	d := &describer{}
	tool := newDescribeAttachedImage(d, files)
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"file":"cat.png"}`))
	if res.Content != "a cat" || string(d.got) != "png" {
		t.Errorf("content %q, read %q", res.Content, d.got)
	}
	if newDescribeAttachedImage(nil, files) != nil || newDescribeAttachedImage(d, files[1:]) != nil {
		t.Error("describe_image offered without vision or without an image")
	}
}

type refuse struct{}

func (refuse) Approve(context.Context, tools.Request) (tools.Decision, error) {
	return tools.Deny, tools.RefusedByRule
}

func TestTheTurnsApproverGatesTheHostsTools(t *testing.T) {
	provider, _ := fakeModel(t, "web_search")
	tool := &lookup{}
	var seen TurnInfo
	srv := start(t, Config{
		Agent: Agent{Name: "partage"}, Provider: provider, Tools: []agent.Tool{tool}, Token: "t",
		Approver: func(_ context.Context, turn TurnInfo) (tools.Approver, error) {
			seen = turn
			return refuse{}, nil
		},
	})

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("quel budget ?", "ctx-gate", map[string]any{SubjectKey: "marie"})))

	if tool.calls.Load() != 0 {
		t.Errorf("a refused tool ran %d times", tool.calls.Load())
	}
	if seen.Subject != "marie" || seen.ContextID != "ctx-gate" {
		t.Errorf("turn = %+v", seen)
	}
}

func TestTurnToolsAreOfferedForTheTurn(t *testing.T) {
	provider, _ := fakeModel(t, "web_search")
	tool := &lookup{}
	srv := start(t, Config{
		Agent: Agent{Name: "partage"}, Provider: provider, Token: "t",
		TurnTools: func(context.Context, TurnInfo) ([]agent.Tool, error) { return []agent.Tool{tool}, nil },
	})

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("quel budget ?", "ctx-turn", nil)))

	if tool.calls.Load() != 1 {
		t.Errorf("the turn's tool ran %d times", tool.calls.Load())
	}
}
