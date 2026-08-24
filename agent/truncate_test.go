package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestTruncateMiddleLeavesShortInputAlone(t *testing.T) {
	in := "short output\n"
	out, truncated := TruncateMiddle(in, 1000)
	if truncated {
		t.Fatal("truncated = true for input under the budget")
	}
	if out != in {
		t.Fatalf("output changed: %q", out)
	}
}

func TestTruncateMiddleRespectsBudget(t *testing.T) {
	in := strings.Repeat("some line of build output\n", 500)
	out, truncated := TruncateMiddle(in, 800)
	if !truncated {
		t.Fatal("truncated = false for input over the budget")
	}
	if len(out) > 800 {
		t.Fatalf("output is %d bytes, want at most 800", len(out))
	}
}

func TestTruncateMiddleKeepsHeadAndTail(t *testing.T) {
	var b strings.Builder
	for i := range 500 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	out, _ := TruncateMiddle(b.String(), 400)
	if !strings.Contains(out, "line 0") {
		t.Fatal("head was dropped")
	}
	if !strings.Contains(out, "line 499") {
		t.Fatal("tail was dropped, but failures report their verdict last")
	}
}

func TestTruncateMiddleAnnouncesTheCut(t *testing.T) {
	out, _ := TruncateMiddle(strings.Repeat("x\n", 5000), 300)
	if !strings.Contains(out, "truncated") {
		t.Fatalf("cut output does not say it was cut: %q", out)
	}
}

func TestTruncateMiddleFavorsTailOverHead(t *testing.T) {
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "%04d filler line\n", i)
	}
	out, _ := TruncateMiddle(b.String(), 1000)
	parts := strings.SplitN(out, "truncated]", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected shape: %q", out)
	}
	if len(parts[1]) <= len(parts[0]) {
		t.Fatalf("tail (%d bytes) is not larger than head (%d bytes)", len(parts[1]), len(parts[0]))
	}
}

func TestTruncateMiddleHandlesZeroBudget(t *testing.T) {
	in := "content"
	out, truncated := TruncateMiddle(in, 0)
	if truncated || out != in {
		t.Fatal("a zero budget should disable truncation rather than empty the result")
	}
}
