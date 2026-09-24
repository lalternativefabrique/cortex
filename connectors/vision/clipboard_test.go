package vision

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// clipboardHoldsImage says whether this machine can answer the question at
// all, so a missing pasteboard reads as "not tested here" rather than as a
// failure of the code.
func clipboardHoldsImage(t *testing.T) bool {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return false
	}
	err := exec.Command("osascript", "-e", `the clipboard as «class PNGf»`).Run()
	return err == nil
}

func TestClipboardImageIsWrittenWhereAsked(t *testing.T) {
	if !clipboardHoldsImage(t) {
		t.Skip("no image in the clipboard on this machine")
	}
	dir := t.TempDir()
	path, err := ClipboardImage(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("an empty file was written")
	}
	header := make([]byte, 8)
	f, _ := os.Open(path)
	defer f.Close()
	f.Read(header)
	if string(header[1:4]) != "PNG" {
		t.Fatalf("header = %q, want a PNG", header)
	}
}

func TestNoImageIsNotAFailure(t *testing.T) {
	// The operator may simply not have taken the screenshot yet, which the
	// caller should report as such rather than as a broken clipboard.
	if runtime.GOOS != "darwin" {
		t.Skip("clipboard reading is macOS-only for now")
	}
	if err := exec.Command("osascript", "-e", `set the clipboard to "plain text"`).Run(); err != nil {
		t.Skip("cannot set the clipboard here")
	}
	_, err := ClipboardImage(t.TempDir())
	if !errors.Is(err, ErrNoImageInClipboard) {
		t.Fatalf("err = %v, want it named as an empty clipboard", err)
	}
}
