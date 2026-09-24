package vision

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ErrNoImageInClipboard says the clipboard holds no image, which is a normal
// outcome — the operator may simply not have taken the screenshot yet.
var ErrNoImageInClipboard = errors.New("no image in the clipboard")

// clipboardTimeout bounds a reader that would otherwise wait for a selection
// owner that never answers.
const clipboardTimeout = 3 * time.Second

// ClipboardImage writes whatever image the clipboard holds to a file and
// returns its path.
//
// A screenshot is the one thing an operator wants to show that has no path:
// a grab shortcut puts it straight in the clipboard, never on disk. Asking
// them to save it first defeats the point of taking it that way.
func ClipboardImage(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "clipboard.png")

	// A reader can sit there forever when the clipboard holds nothing it can
	// convert, and a keystroke that freezes the session is worse than one
	// that says no.
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	cmd, err := clipboardCommand(ctx, path)
	if err != nil {
		return "", err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("%w (%s)", ErrNoImageInClipboard, firstLine(out))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", ErrNoImageInClipboard
	}
	return path, nil
}

// clipboardCommand is what reads the pasteboard on this machine.
//
// Each of these is a small program the desktop already ships, which is what
// keeps this free of a cgo clipboard binding.
func clipboardCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	if runtime.GOOS == "darwin" {
		// osascript errors rather than writing an empty file when the
		// clipboard holds text.
		return exec.CommandContext(ctx, "osascript", "-e", fmt.Sprintf(`
			set imageData to the clipboard as «class PNGf»
			set outFile to open for access POSIX file %q with write permission
			set eof outFile to 0
			write imageData to outFile
			close access outFile
		`, path)), nil
	}

	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			return shellTo(ctx, path, "wl-paste --no-newline --type image/png"), nil
		}
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return shellTo(ctx, path, "xclip -selection clipboard -t image/png -o"), nil
	}
	if _, err := exec.LookPath("wl-paste"); err == nil {
		return shellTo(ctx, path, "wl-paste --no-newline --type image/png"), nil
	}
	return nil, errors.New("no clipboard reader found: install wl-clipboard on wayland, or xclip on x11")
}

// shellTo runs a reader and redirects what it prints into path.
func shellTo(ctx context.Context, path, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command+" > "+shellQuote(path))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}
