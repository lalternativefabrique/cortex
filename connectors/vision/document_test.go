package vision

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// THE property this guards: a PDF travels as a file the provider renders
// itself. Sending it as image_url is refused by every provider, and nothing
// here rasterises pages.
func TestReadDocumentSendsAPDFAsAFileNotAnImage(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"choices":[{"message":{"content":"# Titre"}}]}`))
	}))
	defer srv.Close()

	d, err := New(Config{BaseURL: srv.URL, Model: "test-model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := d.ReadDocument(context.Background(), []byte("%PDF-1.4 fake"), "application/pdf", "Transcris.", 4000); err != nil {
		t.Fatalf("ReadDocument: %v", err)
	}

	raw, _ := json.Marshal(body)
	sent := string(raw)
	if strings.Contains(sent, "image_url") {
		t.Errorf("the PDF was sent as an image:\n%s", sent)
	}
	if !strings.Contains(sent, `"type":"file"`) {
		t.Errorf("no file part in the request:\n%s", sent)
	}
	if !strings.Contains(sent, "data:application/pdf;base64,") {
		t.Errorf("the PDF is not carried as a data URL:\n%s", sent)
	}
}

func TestReadDocumentUsesTheCallersOwnInstructions(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	d, _ := New(Config{BaseURL: srv.URL, Model: "test-model"})
	d.ReadDocument(context.Background(), []byte("pdf"), "application/pdf", "Transcris en Markdown.", 4000)

	raw, _ := json.Marshal(body)
	sent := string(raw)
	// The screenshot-for-a-coding-agent prompt must not steer a transcription.
	if strings.Contains(sent, "coding agent") {
		t.Errorf("the screenshot system prompt leaked into a document read:\n%s", sent)
	}
	if !strings.Contains(sent, "Transcris en Markdown") {
		t.Errorf("the caller's instructions were not sent:\n%s", sent)
	}
}

// An image must keep travelling as an image: this change adds a path, it does
// not move the existing one.
func TestDescribeBytesStillSendsAnImageAsAnImage(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"choices":[{"message":{"content":"a screenshot"}}]}`))
	}))
	defer srv.Close()

	d, _ := New(Config{BaseURL: srv.URL, Model: "test-model"})
	if _, err := d.DescribeBytes(context.Background(), []byte("png"), "image/png", "What is this?"); err != nil {
		t.Fatalf("DescribeBytes: %v", err)
	}

	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), "image_url") {
		t.Errorf("an image no longer travels as one:\n%s", raw)
	}
}
