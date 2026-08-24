package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// EditConfig configures the edit tool.
type EditConfig struct {
	Root     string
	Tracker  *ReadTracker
	Approver Approver
}

type editArgs struct {
	Path       string `json:"path" jsonschema:"description=File path relative to the workspace root."`
	OldString  string `json:"old_string" jsonschema:"description=Exact text to replace, copied verbatim from the file including indentation. Must appear exactly once unless replace_all is set."`
	NewString  string `json:"new_string" jsonschema:"description=Text to put in its place. Use an empty string to delete."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace every occurrence instead of requiring a unique match."`
}

type editTool struct {
	cfg EditConfig
}

// NewEdit returns a tool that replaces exact text in a file.
func NewEdit(cfg EditConfig) agent.Tool {
	if cfg.Approver == nil {
		cfg.Approver = AllowAll{}
	}
	return &editTool{cfg: cfg}
}

func (t *editTool) Name() string { return "edit" }

func (t *editTool) Description() string {
	return strings.Join([]string{
		"Change part of an existing file by replacing exact text.",
		"",
		"This is the tool for modifying code. Read the file first — the edit is refused otherwise, and the read is what shows you the exact text to match.",
		"",
		"old_string must reproduce the file byte for byte, including indentation and surrounding blank lines, but WITHOUT the line-number prefix the read tool adds for display.",
		"It must match exactly one place in the file. If it matches nothing the edit is refused; if it matches several the edit is refused and you are told how many — extend old_string with neighbouring lines until it is unique, or set replace_all when you genuinely mean every occurrence.",
		"Refusing an ambiguous match is deliberate: a replacement applied to the wrong occurrence is a silent corruption, and this is what prevents it.",
		"",
		"To create a new file use write instead. To replace a whole file's contents, prefer several targeted edits — they are easier to verify than a wholesale rewrite.",
		"",
		"Returns a short confirmation with the number of replacements. Does not return the resulting file: read it again if you need to confirm the surrounding context.",
	}, "\n")
}

func (t *editTool) InputSchema() any { return editArgs{} }

func (t *editTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args editArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if args.Path == "" {
		return failure("path is required")
	}
	if args.OldString == "" {
		return failure("old_string is required; use write to create a file")
	}
	if args.OldString == args.NewString {
		return failure("old_string and new_string are identical, so this edit would change nothing")
	}

	abs, err := resolveWithinRoot(t.cfg.Root, args.Path)
	if err != nil {
		return failure("%v", err)
	}
	display := relativeToRoot(t.cfg.Root, abs)

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return failure("%s does not exist; use write to create it", display)
		}
		return failure("%v", err)
	}

	if t.cfg.Tracker != nil {
		readAt, seen := t.cfg.Tracker.ReadAt(abs)
		if !seen {
			return failure("%s has not been read in this session; read it first so the edit matches its current content", display)
		}
		if info.ModTime().After(readAt) {
			return failure("%s changed on disk after it was read; read it again before editing", display)
		}
	}

	content, err := os.ReadFile(abs)
	if err != nil {
		return failure("%v", err)
	}
	text := string(content)

	count := strings.Count(text, args.OldString)
	switch {
	case count == 0:
		return failure("old_string was not found in %s; read the file again and copy the text exactly, without the line-number prefix", display)
	case count > 1 && !args.ReplaceAll:
		return failure("old_string matches %d places in %s; add surrounding lines to make it unique, or set replace_all to change all of them", count, display)
	}

	refused, err := approve(ctx, t.cfg.Approver, Request{
		Tool:   t.Name(),
		Action: fmt.Sprintf("edit %s (%d replacement(s))", display, count),
		Scope:  "edit:" + abs,
		Detail: renderEditPreview(args.OldString, args.NewString),
	})
	if err != nil {
		return failure("approval failed: %v", err)
	}
	if refused != "" {
		return failure("%s: editing %s", refused, display)
	}

	var updated string
	if args.ReplaceAll {
		updated = strings.ReplaceAll(text, args.OldString, args.NewString)
	} else {
		updated = strings.Replace(text, args.OldString, args.NewString, 1)
		count = 1
	}

	if err := writeFilePreservingMode(abs, []byte(updated), info); err != nil {
		return failure("write %s: %v", display, err)
	}
	if t.cfg.Tracker != nil {
		if newInfo, err := os.Stat(abs); err == nil {
			t.cfg.Tracker.MarkRead(abs, newInfo.ModTime())
		}
	}

	return agent.ToolResult{
		Content: fmt.Sprintf("Edited %s: %d replacement(s).", display, count),
		Metadata: map[string]any{
			"ok":           true,
			"path":         abs,
			"replacements": count,
		},
	}, nil
}

// renderEditPreview builds a compact before/after for the approval prompt.
func renderEditPreview(old, updated string) string {
	const maxSide = 600
	return "- " + strings.ReplaceAll(clip(old, maxSide), "\n", "\n- ") +
		"\n+ " + strings.ReplaceAll(clip(updated, maxSide), "\n", "\n+ ")
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// writeFilePreservingMode replaces a file's content while keeping its
// permission bits, which a plain create would reset.
func writeFilePreservingMode(path string, data []byte, info os.FileInfo) error {
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, data, mode)
}
