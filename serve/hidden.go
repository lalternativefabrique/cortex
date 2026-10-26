package serve

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lalternative/packages/go/cortex/tools"
)

// hiddenRefused keeps an assistant out of the dot-files of the folder it
// reads: keys, tokens and settings live there, never what a person asks
// about.
type hiddenRefused struct {
	root string
	next tools.Approver
}

func (h hiddenRefused) Approve(ctx context.Context, req tools.Request) (tools.Decision, error) {
	if path, ok := strings.CutPrefix(req.Scope, "read:"); ok && isHidden(h.root, path) {
		return tools.Deny, nil
	}
	if h.next == nil {
		return tools.Allow, nil
	}
	return h.next.Approve(ctx, req)
}

func isHidden(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}
