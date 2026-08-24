// Package promptctx assembles the prompt sent to the model.
//
// Assembly order is the point: the system prompt and project conventions
// come first and never vary within a session, so an inference server can
// reuse its KV cache across every step of a run. Volatile facts — git state,
// the directory listing — go last, where a change invalidates only the tail.
// Putting a clock or a commit hash near the front would silently defeat
// caching for the whole conversation.
package promptctx

import (
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed system.md
var promptFS embed.FS

// SystemPrompt returns the embedded base instructions.
func SystemPrompt() (string, error) {
	b, err := promptFS.ReadFile("system.md")
	if err != nil {
		return "", fmt.Errorf("read embedded system prompt: %w", err)
	}
	return string(b), nil
}

// Options configures assembly.
type Options struct {
	Root string
	// ConventionFiles are candidate names for the project's own instructions,
	// tried in order. Zero value means DefaultConventionFiles.
	ConventionFiles []string
	// Override replaces the embedded system prompt entirely when non-empty.
	Override string
}

// DefaultConventionFiles are the conventional names for per-project agent
// instructions.
var DefaultConventionFiles = []string{"AGENTS.md", "CLAUDE.md", ".ai/instructions.md"}

// System builds the stable prefix: base instructions plus any project
// conventions found under Root. Nothing volatile belongs in the result.
func System(opts Options) (string, error) {
	base := opts.Override
	if base == "" {
		var err error
		base, err = SystemPrompt()
		if err != nil {
			return "", err
		}
	}

	names := opts.ConventionFiles
	if names == nil {
		names = DefaultConventionFiles
	}
	found := readAll(opts.Root, names)
	if len(found) == 0 {
		return base, nil
	}

	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n## Project conventions\n\n")
	b.WriteString("These come from the repository itself and take precedence over the general guidance above. Where two of them disagree, the longer and more specific one is usually the current one; say so rather than following the stale one silently.\n")
	for _, f := range found {
		fmt.Fprintf(&b, "\n--- %s ---\n\n%s\n", f.name, strings.TrimSpace(f.content))
	}
	// The conventions are reference, and reference read last is mistaken for
	// the task: asked whether it speaks French, an agent whose prompt ended on
	// a list of deploy commands went on writing that list. What it is for
	// comes after them, so the last thing read is the instruction.
	b.WriteString("\n--- end of project conventions ---\n\n")
	b.WriteString("Those files describe the repository. They are reference, not something to restate or continue — answer whatever is asked, and read them when the question calls for it.\n")
	return b.String(), nil
}

// Workspace renders the volatile facts about the repository: where it is,
// what branch it is on, and what is currently modified. It belongs in the
// first user message, after the stable prefix.
func Workspace(root string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Workspace: %s\n", root)

	if branch := branchName(root); branch != "" {
		fmt.Fprintf(&b, "Branch: %s\n", branch)
	}
	if status, err := git(root, "status", "--porcelain"); err == nil {
		if status == "" {
			b.WriteString("Working tree: clean\n")
		} else {
			lines := strings.Split(status, "\n")
			fmt.Fprintf(&b, "Working tree: %d modified file(s)\n", len(lines))
			const maxListed = 20
			for i, line := range lines {
				if i == maxListed {
					fmt.Fprintf(&b, "  ... and %d more\n", len(lines)-maxListed)
					break
				}
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	if tree := topLevel(root); tree != "" {
		fmt.Fprintf(&b, "\nTop level:\n%s", tree)
	}
	return b.String()
}

type conventionFile struct {
	name    string
	content string
}

// readAll returns every convention file present, not the first match.
//
// A repository often carries several — AGENTS.md for one tool, CLAUDE.md for
// another — and they drift apart. Taking only the first silently hides the
// others, including the one that happens to be current.
func readAll(root string, names []string) []conventionFile {
	var out []conventionFile
	seen := map[string]struct{}{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(root, n))
		if err != nil || len(b) == 0 {
			continue
		}
		// A case-insensitive filesystem resolves AGENTS.md and agents.md to
		// the same file; including it twice would double its weight.
		key := strings.ToLower(n)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, conventionFile{name: n, content: string(b)})
	}
	return out
}

// branchName resolves the current branch, falling back to the symbolic ref
// on a repository with no commit yet: rev-parse fails there because HEAD
// points at a branch that does not exist, which is the normal state of a
// freshly initialised repository.
func branchName(root string) string {
	if branch, err := git(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		return branch
	}
	if ref, err := git(root, "symbolic-ref", "--short", "HEAD"); err == nil {
		return ref + " (no commits yet)"
	}
	return ""
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// topLevel lists the root's immediate entries, which orients the model
// without the cost of a full tree.
func topLevel(root string) string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && name != ".sklp" && name != ".github" {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintf(&b, "  %s\n", name)
	}
	return b.String()
}
