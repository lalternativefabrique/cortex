// Package tools implements the capabilities a coding agent needs: reading,
// searching, editing and writing files, and running commands.
//
// Every tool is confined to a declared root, and every recoverable failure —
// a missing file, an ambiguous edit, a non-zero exit — is returned to the
// model as readable text rather than as a Go error, so the model corrects
// itself instead of the run aborting.
package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// resolveWithinRoot resolves rel under root and returns an absolute path,
// refusing anything that escapes root.
//
// rel may be relative or absolute: models routinely echo back an absolute
// path they read from an earlier tool result, and rejecting that shape
// produces failures that teach the model nothing.
func resolveWithinRoot(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	var target string
	switch {
	case rel == "":
		target = absRoot
	case filepath.IsAbs(rel):
		target = filepath.Clean(rel)
	default:
		target = filepath.Join(absRoot, rel)
	}
	clean := filepath.Clean(target)
	if clean != absRoot && !strings.HasPrefix(clean, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	return clean, nil
}

// relativeToRoot renders an absolute path for display, preferring the
// workspace-relative form.
func relativeToRoot(root, abs string) string {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return abs
	}
	return rel
}

// failure returns a recoverable error as a tool result the model can act on.
func failure(format string, args ...any) (agent.ToolResult, error) {
	return agent.ToolResult{
		Content:  "error: " + fmt.Sprintf(format, args...),
		Metadata: map[string]any{"ok": false},
	}, nil
}
