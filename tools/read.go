package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// ReadConfig configures the read tool.
type ReadConfig struct {
	Root string
	// MaxLines caps a single read. Zero means DefaultReadMaxLines.
	MaxLines int
	// MaxLineBytes truncates individual long lines, so a minified bundle
	// cannot consume the context in one line. Zero means
	// DefaultMaxLineBytes.
	MaxLineBytes int
	// Tracker records which files were read, so edit can require that a file
	// was seen before it is changed. Optional.
	Tracker *ReadTracker
}

const (
	DefaultReadMaxLines = 800
	DefaultMaxLineBytes = 2000
	binarySniffBytes    = 8000
)

type readArgs struct {
	Path   string `json:"path" jsonschema:"description=File path relative to the workspace root."`
	Offset int    `json:"offset,omitempty" jsonschema:"description=1-based line number to start from. Use with limit to page through a file too large to read at once."`
	Limit  int    `json:"limit,omitempty" jsonschema:"description=Maximum number of lines to return. Defaults to the tool maximum."`
}

type readTool struct {
	cfg ReadConfig
}

// NewRead returns a tool that reads a file as numbered lines.
func NewRead(cfg ReadConfig) agent.Tool {
	if cfg.MaxLines == 0 {
		cfg.MaxLines = DefaultReadMaxLines
	}
	if cfg.MaxLineBytes == 0 {
		cfg.MaxLineBytes = DefaultMaxLineBytes
	}
	return &readTool{cfg: cfg}
}

func (t *readTool) Name() string { return "read" }

func (t *readTool) Description() string {
	return strings.Join([]string{
		"Read a file from the workspace as numbered lines.",
		"",
		"Use this to inspect any file before editing it: the edit tool requires that a file has been read first, and the line numbers shown here are how you locate the text to change.",
		"Prefer this over running `cat`, `head` or `sed` through bash — those return unnumbered text and cost an extra process.",
		"",
		fmt.Sprintf("Returns at most %d lines per call, each prefixed with its 1-based line number and a tab. Lines longer than %d bytes are cut. When the file is longer than the limit, the result says so; page through the rest with offset and limit.", t.cfg.MaxLines, t.cfg.MaxLineBytes),
		"The line-number prefix is display only — it is not part of the file content, so never include it in an edit.",
		"",
		"Does not return: file permissions, ownership, or git status. Refuses binary files and paths outside the workspace.",
	}, "\n")
}

func (t *readTool) InputSchema() any { return readArgs{} }

func (t *readTool) Execute(_ context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args readArgs
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

	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return failure("%s does not exist", relativeToRoot(t.cfg.Root, abs))
		}
		return failure("%v", err)
	}
	if info.IsDir() {
		return failure("%s is a directory; use glob or bash `ls` to list it", relativeToRoot(t.cfg.Root, abs))
	}

	f, err := os.Open(abs)
	if err != nil {
		return failure("%v", err)
	}
	defer f.Close()

	if binary, err := looksBinary(f); err != nil {
		return failure("%v", err)
	} else if binary {
		return failure("%s looks like a binary file (%d bytes); it cannot be read as text", relativeToRoot(t.cfg.Root, abs), info.Size())
	}
	if _, err := f.Seek(0, 0); err != nil {
		return failure("%v", err)
	}

	offset := args.Offset
	if offset < 1 {
		offset = 1
	}
	limit := args.Limit
	if limit <= 0 || limit > t.cfg.MaxLines {
		limit = t.cfg.MaxLines
	}

	var b strings.Builder
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	lineNo, shown := 0, 0
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if shown >= limit {
			break
		}
		line := scanner.Text()
		if len(line) > t.cfg.MaxLineBytes {
			line = line[:t.cfg.MaxLineBytes] + fmt.Sprintf(" ... [line cut, %d bytes total]", len(line))
		}
		fmt.Fprintf(&b, "%d\t%s\n", lineNo, line)
		shown++
	}
	if err := scanner.Err(); err != nil {
		return failure("read %s: %v", relativeToRoot(t.cfg.Root, abs), err)
	}

	if shown == 0 {
		if lineNo == 0 {
			return agent.ToolResult{
				Content:  fmt.Sprintf("%s is empty.", relativeToRoot(t.cfg.Root, abs)),
				Metadata: map[string]any{"ok": true, "path": abs, "lines": 0},
			}, nil
		}
		return failure("offset %d is past the end of %s, which has %d lines", offset, relativeToRoot(t.cfg.Root, abs), lineNo)
	}

	// Count the remaining lines only when the page filled up, since that is
	// the only case where the model needs to know there is more to fetch.
	var notice string
	if shown == limit {
		total, err := countLines(abs)
		if err == nil && total > offset+shown-1 {
			notice = fmt.Sprintf("\n[showing lines %d-%d of %d; call read again with offset=%d for more]\n",
				offset, offset+shown-1, total, offset+shown)
		}
	}

	if t.cfg.Tracker != nil {
		t.cfg.Tracker.MarkRead(abs, info.ModTime())
	}

	return agent.ToolResult{
		Content: b.String() + notice,
		Metadata: map[string]any{
			"ok":    true,
			"path":  abs,
			"lines": shown,
		},
	}, nil
}

// looksBinary reports whether the head of the file contains a NUL byte,
// which no text encoding the agent handles will produce.
func looksBinary(f *os.File) (bool, error) {
	buf := make([]byte, binarySniffBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		if err.Error() == "EOF" {
			return false, nil
		}
		return false, err
	}
	for _, c := range buf[:n] {
		if c == 0 {
			return true, nil
		}
	}
	return false, nil
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n, scanner.Err()
}
