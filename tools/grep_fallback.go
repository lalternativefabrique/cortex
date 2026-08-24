package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// searchResult is what a search backend produces before rendering.
type searchResult struct {
	body    string
	matches int
	capped  bool
}

// searchNative walks the tree and matches with Go's own regexp engine.
//
// It exists so the tool works on a machine without ripgrep rather than
// telling the model to go use bash — an instruction that costs a step and
// returns worse-structured output. It is slower and does not read
// .gitignore, so ripgrep is still preferred when present.
func searchNative(ctx context.Context, root, base string, args grepArgs, maxMatches int) (searchResult, error) {
	pattern := args.Pattern
	if !args.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return searchResult{}, fmt.Errorf("pattern is not a valid regular expression: %v", err)
	}

	skip := map[string]struct{}{}
	for _, d := range defaultSkipDirs {
		skip[d] = struct{}{}
	}

	var b strings.Builder
	matches, capped := 0, false

	walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
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
		if args.Glob != "" && !matchGlob(args.Glob, relativeToRoot(root, path)) {
			return nil
		}
		if matches >= maxMatches {
			capped = true
			return filepath.SkipAll
		}

		// One past the budget, so a file that fills it exactly is still
		// known to have more behind it.
		hits, more, err := scanFile(path, re, args.FilesOnly, maxMatches-matches)
		if err != nil {
			return nil
		}
		if more {
			capped = true
		}
		for _, line := range hits {
			fmt.Fprintf(&b, "%s%s\n", relativeToRoot(root, path), line)
			matches++
		}
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return searchResult{}, fmt.Errorf("search cancelled")
	}

	return searchResult{body: b.String(), matches: matches, capped: capped}, nil
}

// scanFile returns the matching lines of one file, formatted as the suffix
// that follows the path in the output, and whether matches were left behind
// because the budget ran out.
func scanFile(path string, re *regexp.Regexp, filesOnly bool, budget int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if strings.IndexByte(line, 0) >= 0 {
			// A NUL byte means this is not text; abandon the file rather
			// than emit binary noise.
			return nil, false, nil
		}
		if !re.MatchString(line) {
			continue
		}
		if filesOnly {
			return []string{""}, false, nil
		}
		out = append(out, fmt.Sprintf(":%d: %s", lineNo, strings.TrimSpace(line)))
		if len(out) >= budget {
			return out, hasFurtherMatch(scanner, re), nil
		}
	}
	return out, false, scanner.Err()
}

// hasFurtherMatch reports whether at least one more line would have matched,
// so the caller can say the result was cut instead of implying it is
// complete.
func hasFurtherMatch(scanner *bufio.Scanner, re *regexp.Regexp) bool {
	for scanner.Scan() {
		if re.MatchString(scanner.Text()) {
			return true
		}
	}
	return false
}
