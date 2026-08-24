package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// onePixelPNG is the smallest valid PNG, used so tests exercise real bytes
// rather than a made-up blob.
var onePixelPNG, _ = base64.StdEncoding.DecodeString(
	"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

func writeImage(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureServer records what the describer sent and replies with a canned
// description.
func captureServer(t *testing.T, reply string, status int) (*httptest.Server, *map[string]any) {
	t.Helper()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			w.Write([]byte(`{"error":{"message":"` + reply + `"}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func newTestDescriber(t *testing.T, url string) *Describer {
	t.Helper()
	d, err := New(Config{BaseURL: url + "/v1", Model: "vision-test", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDescribeReturnsModelText(t *testing.T) {
	srv, _ := captureServer(t, "A terminal showing: FAIL ledger_test.go:39", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	got, err := d.Describe(context.Background(), path, "what error is shown?")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "ledger_test.go:39") {
		t.Fatalf("description = %q", got)
	}
}

func TestDescribeSendsImageAsDataURI(t *testing.T) {
	srv, captured := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	if _, err := d.Describe(context.Background(), path, "q"); err != nil {
		t.Fatal(err)
	}

	messages := (*captured)["messages"].([]any)
	user := messages[len(messages)-1].(map[string]any)
	parts := user["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("got %d content parts, want text and image", len(parts))
	}
	image := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("second part type = %v", image["type"])
	}
	url := image["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image was not inlined as a data URI: %.40s", url)
	}
	if image["image_url"].(map[string]any)["detail"] != "high" {
		t.Fatal("detail is not high; downscaling loses the small text a coding agent needs")
	}
}

func TestDescribePassesTheQuestion(t *testing.T) {
	srv, captured := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	if _, err := d.Describe(context.Background(), path, "which components are used?"); err != nil {
		t.Fatal(err)
	}
	messages := (*captured)["messages"].([]any)
	user := messages[len(messages)-1].(map[string]any)
	text := user["content"].([]any)[0].(map[string]any)["text"].(string)
	if text != "which components are used?" {
		t.Fatalf("question = %q, want it forwarded verbatim", text)
	}
}

func TestDescribeFallsBackWhenNoQuestion(t *testing.T) {
	srv, captured := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	if _, err := d.Describe(context.Background(), path, "   "); err != nil {
		t.Fatal(err)
	}
	messages := (*captured)["messages"].([]any)
	user := messages[len(messages)-1].(map[string]any)
	if text := user["content"].([]any)[0].(map[string]any)["text"].(string); text == "" {
		t.Fatal("an empty question produced an empty prompt")
	}
}

func TestSystemPromptDemandsVerbatimText(t *testing.T) {
	srv, captured := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	if _, err := d.Describe(context.Background(), path, "q"); err != nil {
		t.Fatal(err)
	}
	messages := (*captured)["messages"].([]any)
	system := messages[0].(map[string]any)
	if system["role"] != "system" {
		t.Fatal("no system turn was sent")
	}
	// A paraphrased identifier sends the agent hunting for a symbol that
	// does not exist, so this instruction is load-bearing.
	if !strings.Contains(system["content"].(string), "exactly as it appears") {
		t.Fatal("the system prompt does not demand verbatim transcription")
	}
}

func TestDescribeRejectsUnsupportedFormat(t *testing.T) {
	srv, _ := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "notes.txt", []byte("hello"))

	_, err := d.Describe(context.Background(), path, "q")
	if err == nil {
		t.Fatal("a text file was accepted as an image")
	}
	if !strings.Contains(err.Error(), "readable image") {
		t.Fatalf("error does not say what is accepted: %v", err)
	}
}

func TestDescribeReportsMissingFile(t *testing.T) {
	srv, _ := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)

	_, err := d.Describe(context.Background(), filepath.Join(t.TempDir(), "gone.png"), "q")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want a clear missing-file message", err)
	}
}

func TestDescribeShrinksAnOversizedImage(t *testing.T) {
	// An image over the budget used to be refused with "resize it first".
	// Since every retina screenshot is over it, the budget is now something
	// to fit into rather than a wall.
	srv, captured := captureServer(t, "ok", http.StatusOK)
	d, err := New(Config{BaseURL: srv.URL + "/v1", Model: "m", MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}

	big := image.NewRGBA(image.Rect(0, 0, 400, 300))
	seed := uint32(7)
	for y := 0; y < 300; y++ {
		for x := 0; x < 400; x++ {
			seed = seed*1664525 + 1013904223
			big.Set(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(seed >> 8), 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, big); err != nil {
		t.Fatal(err)
	}
	path := writeImage(t, t.TempDir(), "big.png", raw.Bytes())

	if _, err := d.Describe(context.Background(), path, "q"); err != nil {
		t.Fatalf("an oversized image was refused rather than shrunk: %v", err)
	}
	body, err := json.Marshal(*captured)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "data:image/jpeg;base64,") {
		t.Fatal("the image was not re-encoded on the way out")
	}
}

func TestDescribeRejectsWhatIsNotAnImage(t *testing.T) {
	srv, _ := captureServer(t, "ok", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "notreally.png", make([]byte, 4000))

	_, err := d.Describe(context.Background(), path, "q")
	if err == nil {
		t.Fatal("a file that is not an image was sent anyway")
	}
	if !strings.Contains(err.Error(), "not a readable image") {
		t.Fatalf("error does not say what is wrong: %v", err)
	}
}

func TestDescribeReportsEmptyAnswer(t *testing.T) {
	srv, _ := captureServer(t, "   ", http.StatusOK)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	_, err := d.Describe(context.Background(), path, "q")
	if err == nil {
		t.Fatal("an empty description was accepted")
	}
	// A text-only model accepts the payload and ignores the image, so this
	// is the usual symptom of the wrong model being configured.
	if !strings.Contains(err.Error(), "vision-capable") {
		t.Fatalf("error does not point at the likely cause: %v", err)
	}
}

func TestDescribeReportsServerError(t *testing.T) {
	srv, _ := captureServer(t, "model not found", http.StatusNotFound)
	d := newTestDescriber(t, srv.URL)
	path := writeImage(t, t.TempDir(), "shot.png", onePixelPNG)

	_, err := d.Describe(context.Background(), path, "q")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the status surfaced", err)
	}
}

func TestNewRequiresModelAndURL(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Fatal("a describer with no endpoint was accepted")
	}
	if _, err := New(Config{BaseURL: "http://x/v1"}); err == nil {
		t.Fatal("a describer with no model was accepted")
	}
}

func TestFormatComesFromContentNotExtension(t *testing.T) {
	dir := t.TempDir()
	// PNG bytes under a .jpg name: themedia type must follow what decodes, not
	// what the file claims, or the provider receives a contradiction.
	path := writeImage(t, dir, "mislabelled.jpg", onePixelPNG)

	uri, err := encodeImage(path, DefaultMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("got %.30s, want the decoded format to win over the extension", uri)
	}
}

func TestEncodeRejectsNonImage(t *testing.T) {
	// A .png name on bytes that are not an image: caught here rather than by
	// a provider error later.
	path := writeImage(t, t.TempDir(), "fake.png", []byte("this is just text"))

	_, err := encodeImage(path, DefaultMaxBytes)
	if err == nil {
		t.Fatal("a non-image file was encoded and would have been sent")
	}
	if !strings.Contains(err.Error(), "not a readable image") {
		t.Fatalf("error does not explain the problem: %v", err)
	}
}

func TestEncodeIgnoresExtensionCase(t *testing.T) {
	path := writeImage(t, t.TempDir(), "SHOT.PNG", onePixelPNG)
	if _, err := encodeImage(path, DefaultMaxBytes); err != nil {
		t.Fatalf("an uppercase extension was rejected: %v", err)
	}
}

func TestALargeImageIsShrunkRatherThanRefused(t *testing.T) {
	// A retina screenshot runs to several megabytes, which is every
	// screenshot the operator will take. Telling them to resize it first is
	// asking them to do what this can do.
	// Noise, not a gradient: png compresses a smooth image below the limit
	// and the case under test never arises.
	big := image.NewRGBA(image.Rect(0, 0, 2000, 1500))
	seed := uint32(1)
	for y := 0; y < 1500; y++ {
		for x := 0; x < 2000; x++ {
			seed = seed*1664525 + 1013904223
			big.Set(x, y, color.RGBA{uint8(seed >> 24), uint8(seed >> 16), uint8(seed >> 8), 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, big); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	const limit = 512 * 1024
	uri, err := encodeImage(path, limit)
	if err != nil {
		t.Fatalf("a large image was refused: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("uri starts %.30q, want a jpeg data URI", uri)
	}
	if len(uri) > limit {
		t.Fatalf("encoded to %d bytes, over the %d limit", len(uri), limit)
	}
}

func TestASmallImageIsSentUntouched(t *testing.T) {
	// Re-encoding what already fits would lose detail for nothing.
	small := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var raw bytes.Buffer
	if err := png.Encode(&raw, small); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "small.png")
	if err := os.WriteFile(path, raw.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	uri, err := encodeImage(path, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("uri starts %.30q, want the png kept", uri)
	}
}
