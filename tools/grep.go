package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

// GrepConfig configures the grep tool.
type GrepConfig struct {
	Root string
	// MaxMatches caps how many matching lines are reported. Zero means
	// DefaultGrepMaxMatches.
	MaxMatches int
	Timeout    time.Duration
}

const (
	DefaultGrepMaxMatches = 100
	defaultGrepTimeout    = 30 * time.Second
)

type grepArgs struct {
	Pattern       string `json:"pattern" jsonschema:"description=Regular expression to search for."`
	Path          string `json:"path,omitempty" jsonschema:"description=Directory or file to search, relative to the workspace root. Defaults to the whole workspace."`
	Glob          string `json:"glob,omitempty" jsonschema:"description=Restrict the search to files matching this glob, e.g. *.go or **/*.ts."`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"description=Match case exactly. Defaults to case-insensitive."`
	FilesOnly     bool   `json:"files_only,omitempty" jsonschema:"description=Return only the list of matching file paths, without the matching lines."`
}

type grepTool struct {
	cfg GrepConfig
}

// NewGrep returns a tool that searches file contents.
func NewGrep(cfg GrepConfig) agent.Tool {
	if cfg.MaxMatches == 0 {
		cfg.MaxMatches = DefaultGrepMaxMatches
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultGrepTimeout
	}
	return &grepTool{cfg: cfg}
}

func (t *grepTool) Name() string { return "grep" }

func (t *grepTool) Description() string {
	return strings.Join([]string{
		"Search file contents across the workspace with a regular expression.",
		"",
		"This is how you find where something is defined or used before reading it. Prefer it over running grep or rg through bash: it already skips .gitignored paths and returns a capped, structured result.",
		"",
		"Matching is case-insensitive unless case_sensitive is set. Narrow a broad search with path or glob rather than raising the cap.",
		fmt.Sprintf("Returns at most %d matching lines as `path:line: text`, or just the file paths when files_only is set. When the cap is hit the result says so — refine the pattern instead of paging.", t.cfg.MaxMatches),
		"",
		"Does not return: surrounding context lines, or matches inside ignored directories and binary files. To see a match in context, read the file at the reported line.",
	}, "\n")
}

func (t *grepTool) InputSchema() any { return grepArgs{} }

func (t *grepTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args grepArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if args.Pattern == "" {
		return failure("pattern is required")
	}

	searchPath, err := resolveWithinRoot(t.cfg.Root, args.Path)
	if err != nil {
		return failure("%v", err)
	}

	cctx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	res, err := t.search(cctx, searchPath, args)
	if err != nil {
		if cctx.Err() != nil {
			return failure("search timed out after %s; narrow it with path or glob", t.cfg.Timeout)
		}
		return failure("%v", err)
	}

	if res.matches == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("No match for %q.", args.Pattern),
			Metadata: map[string]any{"ok": true, "matches": 0},
		}, nil
	}

	content := res.body
	if res.capped {
		content += fmt.Sprintf("\n[stopped at %d matches; narrow the pattern, path or glob to see the rest]\n", t.cfg.MaxMatches)
	}
	return agent.ToolResult{
		Content: content,
		Metadata: map[string]any{
			"ok":      true,
			"matches": res.matches,
			"capped":  res.capped,
		},
	}, nil
}

// search prefers ripgrep, which respects .gitignore and is far faster on a
// large tree, and falls back to a native walk when it is not installed.
func (t *grepTool) search(ctx context.Context, base string, args grepArgs) (searchResult, error) {
	if rg, err := exec.LookPath("rg"); err == nil {
		return t.searchRipgrep(ctx, rg, base, args)
	}
	return searchNative(ctx, t.cfg.Root, base, args, t.cfg.MaxMatches)
}

func (t *grepTool) searchRipgrep(ctx context.Context, rg, searchPath string, args grepArgs) (searchResult, error) {

	rgArgs := []string{"--no-heading", "--line-number", "--color", "never"}
	if !args.CaseSensitive {
		rgArgs = append(rgArgs, "--ignore-case")
	}
	if args.FilesOnly {
		rgArgs = append(rgArgs, "--files-with-matches")
	}
	if args.Glob != "" {
		rgArgs = append(rgArgs, "--glob", args.Glob)
	}
	rgArgs = append(rgArgs, "--max-count", fmt.Sprint(t.cfg.MaxMatches), "--", args.Pattern, searchPath)

	out, runErr := exec.CommandContext(ctx, rg, rgArgs...).Output()
	if runErr != nil {
		var ee *exec.ExitError
		if asExit(runErr, &ee) {
			// ripgrep exits 1 when nothing matched, which is a normal result.
			if ee.ExitCode() == 1 {
				return searchResult{}, nil
			}
			return searchResult{}, fmt.Errorf("search failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return searchResult{}, fmt.Errorf("search failed: %v", runErr)
	}

	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return searchResult{}, nil
	}
	lines := strings.Split(trimmed, "\n")
	capped := false
	if len(lines) > t.cfg.MaxMatches {
		lines = lines[:t.cfg.MaxMatches]
		capped = true
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(strings.TrimPrefix(line, t.cfg.Root+"/"))
		b.WriteByte('\n')
	}
	return searchResult{body: b.String(), matches: len(lines), capped: capped}, nil
}

func asExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}
