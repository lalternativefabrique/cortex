package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// WriteConfig configures the write tool.
type WriteConfig struct {
	Root     string
	Tracker  *ReadTracker
	Approver Approver
}

type writeArgs struct {
	Path    string `json:"path" jsonschema:"description=File path relative to the workspace root."`
	Content string `json:"content" jsonschema:"description=Complete file content. This replaces the whole file."`
}

type writeTool struct {
	cfg WriteConfig
}

// NewWrite returns a tool that writes a whole file.
func NewWrite(cfg WriteConfig) agent.Tool {
	if cfg.Approver == nil {
		cfg.Approver = AllowAll{}
	}
	return &writeTool{cfg: cfg}
}

func (t *writeTool) Name() string { return "write" }

func (t *writeTool) Description() string {
	return strings.Join([]string{
		"Create a new file, or replace an existing file's entire content.",
		"",
		"Use this for new files. To change part of a file that already exists, use edit instead — a targeted replacement is safer than rewriting everything, and it will not silently drop code you did not intend to touch.",
		"",
		"Overwriting an existing file requires having read it first, so a rewrite is never based on assumed content. Parent directories are created as needed.",
		"content is written verbatim: include no line-number prefixes and no markdown code fences.",
		"",
		"Returns a short confirmation with the byte count. Does not return the file back.",
	}, "\n")
}

func (t *writeTool) InputSchema() any { return writeArgs{} }

func (t *writeTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args writeArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if args.Path == "" {
		return failure("path is required")
	}

	abs, err := resolveWithinRoot(t.cfg.Root, args.Path)
	if err != nil {
		return failure("%v", err)
	}
	display := relativeToRoot(t.cfg.Root, abs)

	info, statErr := os.Stat(abs)
	exists := statErr == nil
	if exists && info.IsDir() {
		return failure("%s is a directory", display)
	}
	if exists && t.cfg.Tracker != nil {
		readAt, seen := t.cfg.Tracker.ReadAt(abs)
		if !seen {
			return failure("%s already exists and has not been read in this session; read it first, then edit it or overwrite it deliberately", display)
		}
		if info.ModTime().After(readAt) {
			return failure("%s changed on disk after it was read; read it again before overwriting", display)
		}
	}

	action := "create " + display
	if exists {
		action = fmt.Sprintf("overwrite %s (%d bytes)", display, info.Size())
	}
	refused, err := approve(ctx, t.cfg.Approver, Request{
		Tool:   t.Name(),
		Action: action,
		Scope:  "write:" + abs,
		Detail: clip(args.Content, 600),
	})
	if err != nil {
		return failure("approval failed: %v", err)
	}
	if refused != "" {
		return failure("%s: writing %s", refused, display)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return failure("create parent directory: %v", err)
	}
	if err := writeFilePreservingMode(abs, []byte(args.Content), fileInfoOrNil(info, exists)); err != nil {
		return failure("write %s: %v", display, err)
	}
	if t.cfg.Tracker != nil {
		if newInfo, err := os.Stat(abs); err == nil {
			t.cfg.Tracker.MarkRead(abs, newInfo.ModTime())
		}
	}

	verb := "Created"
	if exists {
		verb = "Overwrote"
	}
	return agent.ToolResult{
		Content: fmt.Sprintf("%s %s (%d bytes).", verb, display, len(args.Content)),
		Metadata: map[string]any{
			"ok":      true,
			"path":    abs,
			"bytes":   len(args.Content),
			"created": !exists,
		},
	}, nil
}

func fileInfoOrNil(info os.FileInfo, exists bool) os.FileInfo {
	if exists {
		return info
	}
	return nil
}
