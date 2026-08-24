// Package vision turns images into text a coding agent can work with.
//
// The coding model stays text-only. A separate vision model reads the image
// once and returns a description, which enters the conversation as ordinary
// text. That split is deliberate:
//
//   - Models that see well do not code well, and vice versa. Picking one
//     model for both loses on both.
//   - An image left in the conversation is re-sent on every step. A
//     screenshot is easily 1500 tokens, rejoined twenty times.
//   - Describing once keeps the prompt prefix stable, so the inference
//     server can still reuse its cache.
package vision

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"

	"golang.org/x/image/draw"
	"path/filepath"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

// Config points at the vision model.
//
// BaseURL defaults to the coding model's endpoint, since the common case is
// one provider serving both. It is separate so a vision model hosted
// elsewhere needs no other change.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	// MaxBytes caps the encoded image. Zero means DefaultMaxBytes.
	MaxBytes   int
	HTTPClient *http.Client
}

const (
	DefaultTimeout = 2 * time.Minute
	// DefaultMaxBytes caps the encoded payload. base64 inflates by a third,
	// so a 4 MiB ceiling on the wire is a 3 MiB file — comfortably above any
	// screenshot and well under what a provider will refuse.
	DefaultMaxBytes = 4 << 20
)

// Describer produces a textual description of an image.
type Describer struct {
	cfg    Config
	client *http.Client
}

// New returns a Describer, or an error when the config cannot work.
func New(cfg Config) (*Describer, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("vision: BaseURL is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("vision: Model is required")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Describer{cfg: cfg, client: client}, nil
}

// Model reports which model answers.
func (d *Describer) Model() string { return d.cfg.Model }

// Describe reads an image and returns what the vision model saw.
//
// question steers the description. Without one the model produces a generic
// caption, which is rarely what the caller needed: "what error is shown
// here" and "what components and spacing does this mockup use" call for
// completely different readings of the same pixels.
// DescribeBytes describes an image that never touched the disk — a screenshot
// pasted into a window, which has no path to give.
func (d *Describer) DescribeBytes(ctx context.Context, data []byte, mime, question string) (string, error) {
	if max := d.cfg.MaxBytes; max > 0 && len(data) > max {
		return "", fmt.Errorf("image is %d bytes, over the %d limit", len(data), max)
	}
	if mime == "" {
		mime = "image/png"
	}
	return d.describeEncoded(ctx, "data:"+mime+";base64,"+base64.StdEncoding.EncodeToString(data), question)
}

func (d *Describer) Describe(ctx context.Context, path, question string) (string, error) {
	dataURI, err := encodeImage(path, d.cfg.MaxBytes)
	if err != nil {
		return "", err
	}
	return d.describeEncoded(ctx, dataURI, question)
}

// encodeImage reads a file and returns it as a data URI.
//
// Every OpenAI-compatible endpoint accepts an inline base64 data URI, where a
// remote URL would require the provider to fetch it — impossible for anything
// on a developer's machine.
//
// The format is settled by decoding the file, not by trusting its extension:
// a file that does not decode is not an image, whatever it is named, and
// finding that out here beats finding it out from a provider error.
func encodeImage(path string, maxBytes int) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s does not exist", path)
		}
		return "", err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("%s is not a readable image (png, jpeg or gif): %v", filepath.Base(path), err)
	}

	// A screenshot from a retina display runs to several megabytes, which is
	// every screenshot the operator will take. Refusing it and telling them
	// to resize it first is asking them to do what this can do.
	if info.Size()*4/3 > int64(maxBytes) {
		raw, format, err = shrink(raw, maxBytes)
		if err != nil {
			return "", fmt.Errorf("%s is %d MB, over the %d MB limit, and could not be shrunk: %w",
				filepath.Base(path), info.Size()>>20, maxBytes>>20, err)
		}
	}
	return "data:image/" + format + ";base64," + base64.StdEncoding.EncodeToString(raw), nil
}

// shrink re-encodes an image small enough to send.
//
// JPEG at a middling quality, halving the dimensions until it fits: detail is
// worth less here than getting the picture across at all, and a description
// does not need the pixels a display did.
func shrink(raw []byte, maxBytes int) ([]byte, string, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	bounds := src.Bounds()
	for scale := 1; scale <= 8; scale *= 2 {
		w, h := bounds.Dx()/scale, bounds.Dy()/scale
		if w < 1 || h < 1 {
			break
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
			return nil, "", err
		}
		if buf.Len()*4/3 <= maxBytes {
			return buf.Bytes(), "jpeg", nil
		}
	}
	return nil, "", errors.New("still too large at an eighth of its size")
}
