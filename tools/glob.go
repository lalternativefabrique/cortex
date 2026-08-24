package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// GlobConfig configures the glob tool.
type GlobConfig struct {
	Root string
	// MaxResults caps how many paths are returned. Zero means
	// DefaultGlobMaxResults.
	MaxResults int
	// SkipDirs are directory names pruned during the walk.
	SkipDirs []string
}

const DefaultGlobMaxResults = 200

// defaultSkipDirs are the directories whose contents are never the code the
// agent is asked about, and which dominate a walk when included.
var defaultSkipDirs = []string{
	".git", "node_modules", "vendor", "dist", "build", "target",
	".next", ".nuxt", ".turbo", ".venv", "__pycache__", ".cache",
}

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=Glob pattern such as **/*.go or apps/*/cmd/**. Matched against workspace-relative paths."`
	Path    string `json:"path,omitempty" jsonschema:"description=Directory to search under, relative to the workspace root. Defaults to the whole workspace."`
}

type globTool struct {
	cfg GlobConfig
}

// NewGlob returns a tool that finds files by path pattern.
func NewGlob(cfg GlobConfig) agent.Tool {
	if cfg.MaxResults == 0 {
		cfg.MaxResults = DefaultGlobMaxResults
	}
	if cfg.SkipDirs == nil {
		cfg.SkipDirs = defaultSkipDirs
	}
	return &globTool{cfg: cfg}
}

func (t *globTool) Name() string { return "glob" }

func (t *globTool) Description() string {
	return strings.Join([]string{
		"Find files by path pattern.",
		"",
		"Use this when you know roughly what a file is called or where it lives but not its exact path. To find files by what is inside them, use grep instead.",
		"",
		"`*` matches within one path segment, `**` matches across segments. Patterns are matched against workspace-relative paths, so `**/*_test.go` finds test files at any depth.",
		fmt.Sprintf("Results are sorted most-recently-modified first — the files being worked on surface first — and capped at %d paths.", t.cfg.MaxResults),
		"",
		fmt.Sprintf("Does not search inside: %s. Returns paths only, never file contents.", strings.Join(t.cfg.SkipDirs, ", ")),
	}, "\n")
}

func (t *globTool) InputSchema() any { return globArgs{} }

type globHit struct {
	path    string
	modUnix int64
}

func (t *globTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args globArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if args.Pattern == "" {
		return failure("pattern is required")
	}

	base, err := resolveWithinRoot(t.cfg.Root, args.Path)
	if err != nil {
		return failure("%v", err)
	}

	skip := make(map[string]struct{}, len(t.cfg.SkipDirs))
	for _, d := range t.cfg.SkipDirs {
		skip[d] = struct{}{}
	}

	var hits []globHit
	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is not a reason to abandon the search.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if _, pruned := skip[d.Name()]; pruned && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		if !matchGlob(args.Pattern, rel) && !matchGlob(args.Pattern, relativeToRoot(t.cfg.Root, path)) {
			return nil
		}
		var mod int64
		if info, statErr := d.Info(); statErr == nil {
			mod = info.ModTime().Unix()
		}
		hits = append(hits, globHit{path: relativeToRoot(t.cfg.Root, path), modUnix: mod})
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return failure("search cancelled")
	}

	if len(hits) == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("No file matches %q.", args.Pattern),
			Metadata: map[string]any{"ok": true, "matches": 0},
		}, nil
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].modUnix != hits[j].modUnix {
			return hits[i].modUnix > hits[j].modUnix
		}
		return hits[i].path < hits[j].path
	})

	total := len(hits)
	capped := total > t.cfg.MaxResults
	if capped {
		hits = hits[:t.cfg.MaxResults]
	}

	var b strings.Builder
	for _, h := range hits {
		b.WriteString(h.path)
		b.WriteByte('\n')
	}
	if capped {
		// Reporting the total, not just the cap, is what tells the caller
		// whether the pattern was slightly too broad or wildly so.
		fmt.Fprintf(&b, "\n[%d paths matched; only the %d most recently modified are listed. Narrow the pattern — a result this broad usually means the search is in the wrong place.]\n",
			total, t.cfg.MaxResults)
	}

	return agent.ToolResult{
		Content: b.String(),
		Metadata: map[string]any{
			"ok":      true,
			"matches": len(hits),
			"total":   total,
			"capped":  capped,
		},
	}, nil
}

// matchGlob extends filepath.Match with `**`, which it does not support.
//
// The pattern is split into segments and matched against the path's segments
// recursively: `**` consumes any number of segments, anything else must match
// exactly one.
func matchGlob(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(pattern, name)
		return err == nil && ok
	}
	return matchSegments(
		strings.Split(strings.Trim(pattern, "/"), "/"),
		strings.Split(strings.Trim(name, "/"), "/"),
	)
}

func matchSegments(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			// A trailing ** matches whatever remains, including nothing.
			if len(rest) == 0 {
				return true
			}
			// Otherwise try every split point: ** may consume any number of
			// segments before the rest of the pattern picks up.
			for i := 0; i <= len(path); i++ {
				if matchSegments(rest, path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 {
			return false
		}
		ok, err := filepath.Match(pattern[0], path[0])
		if err != nil || !ok {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	return len(path) == 0
}
