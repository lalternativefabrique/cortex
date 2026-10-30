package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// RefusedProtected is a write to what decides the agent's own permissions or
// runs later on the person's behalf: no mode, rule or answer lifts it, since
// an agent that can rewrite its rules can grant itself anything.
const RefusedProtected Refusal = "this file governs the agent's own permissions or runs on its own; only the person edits it, outside the agent"

// protectedInRoot are paths under the workspace root, matched as a prefix.
var protectedInRoot = []string{
	".ai/settings.yaml",
	".ai/settings.local.yaml",
	".ai/mcp.yaml",
	".claude",
	".codex",
	".git/hooks",
	".git/config",
}

// protectedInHome are paths under the person's home, matched as a prefix.
var protectedInHome = []string{
	".config",
	".ssh",
	".bashrc", ".zshrc", ".zshenv", ".profile", ".bash_profile", ".zprofile",
	".gitconfig",
}

// shellWriters are the programs a shell stage writes a file with; a stage
// running one of them, or redirecting its output, and naming a protected
// path is refused. It only catches the usual spellings: the sandbox is what
// holds against the rest.
var shellWriters = map[string]bool{
	"tee": true, "cp": true, "mv": true, "rm": true, "ln": true, "install": true,
	"truncate": true, "dd": true, "chmod": true, "chown": true, "touch": true,
}

func writesProtected(stage string) bool {
	if !shellStageWrites(stage) {
		return false
	}
	for _, p := range protectedInRoot {
		if strings.Contains(stage, p) {
			return true
		}
	}
	for _, p := range protectedInHome {
		for _, home := range []string{"~/", "$HOME/", "${HOME}/"} {
			if strings.Contains(stage, home+p) {
				return true
			}
		}
		if h, err := os.UserHomeDir(); err == nil && strings.Contains(stage, filepath.Join(h, p)) {
			return true
		}
	}
	return false
}

// resolveSymlinks resolves the longest existing prefix of path, so a link
// inside the workspace cannot point a write at a file outside it, and a file
// not yet created still resolves through its existing parents.
func resolveSymlinks(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

func shellStageWrites(stage string) bool {
	fields := strings.Fields(stage)
	for i, f := range fields {
		if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(f, ">&", ""), "2>/dev/null", ""), ">") {
			return true
		}
		if shellWriters[filepath.Base(f)] {
			return true
		}
		if (f == "sed" || f == "perl") && i+1 < len(fields) && strings.HasPrefix(fields[i+1], "-i") {
			return true
		}
	}
	return false
}

func (g *GatedApprover) touchesProtected(req Request) bool {
	for _, prefix := range []string{"write:", "edit:"} {
		if path, ok := strings.CutPrefix(req.Scope, prefix); ok {
			return isProtectedPath(resolveSymlinks(path), g.Root)
		}
	}
	if req.Tool != "bash" {
		return false
	}
	for _, stage := range splitStages(req.Action) {
		if writesProtected(stage) {
			return true
		}
	}
	return false
}

func isProtectedPath(abs, root string) bool {
	if root != "" {
		if absRoot, err := filepath.Abs(root); err == nil {
			for _, p := range protectedInRoot {
				if underOrEqual(abs, filepath.Join(resolveSymlinks(absRoot), p)) {
					return true
				}
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range protectedInHome {
			if underOrEqual(abs, filepath.Join(resolveSymlinks(home), p)) {
				return true
			}
		}
	}
	return false
}

func underOrEqual(path, base string) bool {
	return path == base || strings.HasPrefix(path, base+string(filepath.Separator))
}
