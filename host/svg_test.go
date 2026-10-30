package host

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanSVGKeepsWhatDrawsAndDropsWhatRuns(t *testing.T) {
	in := `<?xml version="1.0"?>
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 200 100" onload="alert(1)">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#333"/></linearGradient></defs>
  <script>alert('x')</script>
  <foreignObject width="10" height="10"><div>html</div></foreignObject>
  <image href="https://evil.example/pixel.png" width="1" height="1"/>
  <rect x="1" y="1" width="50" height="20" fill="url(#g)" onclick="steal()" style="stroke:red;background:url(https://evil)"/>
  <use xlink:href="#g"/><use href="https://evil.example/x.svg#a"/>
  <a href="https://evil.example"><text x="10" y="50" font-size="14">Plan &amp; coupe</text></a>
  <path d="M0 0 L10 10" fill="url(https://evil.example/p)"/>
</svg>`
	out, err := CleanSVG([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"<script", "foreignObject", "<image", "onload", "onclick", "evil.example", "background:url", "<a "} {
		if strings.Contains(s, bad) {
			t.Errorf("output still carries %q:\n%s", bad, s)
		}
	}
	for _, good := range []string{`xmlns="http://www.w3.org/2000/svg"`, `viewBox="0 0 200 100"`, `<linearGradient id="g">`, `fill="url(#g)"`, `<use xlink:href="#g">`, `Plan &amp; coupe`, `font-size="14"`, `<path d="M0 0 L10 10">`} {
		if !strings.Contains(s, good) {
			t.Errorf("output lost %q:\n%s", good, s)
		}
	}
	if strings.Contains(s, "style=") {
		t.Errorf("a style with url() should be dropped whole:\n%s", s)
	}
}

func TestCleanSVGAddsTheNamespaceAndRefusesWhatIsNotADrawing(t *testing.T) {
	out, err := CleanSVG([]byte(`<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4"/></svg>`))
	if err != nil || !strings.HasPrefix(string(out), `<svg xmlns="http://www.w3.org/2000/svg"`) {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, err := CleanSVG([]byte(`<html><body>no</body></html>`)); !errors.Is(err, ErrNotSVG) {
		t.Fatalf("html accepted: %v", err)
	}
	if _, err := CleanSVG([]byte(`<svg><rect`)); !errors.Is(err, ErrNotSVG) {
		t.Fatalf("broken xml accepted: %v", err)
	}
	if _, err := CleanSVG([]byte("<svg>" + strings.Repeat("<rect/>", MaxDrawingBytes/7+1) + "</svg>")); !errors.Is(err, ErrDrawingTooBig) {
		t.Fatalf("oversized drawing accepted: %v", err)
	}
}
