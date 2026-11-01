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

// gitConfigReads are the git config invocations that only print.
var gitConfigReads = []string{"--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--show-origin", "--show-scope"}

// gitKeysThatRun are configuration keys that make a later git command run
// something else; setting one, even for a single command with -c, is
// the same as editing .git/config.
var gitKeysThatRun = []string{
	"core.hookspath", "core.sshcommand", "core.pager", "core.editor", "core.fsmonitor", "core.gitproxy", "core.askpass",
	"alias.", "credential.", "include.", "includeif.", "diff.external", "difftool.", "merge.tool", "mergetool.",
	"filter.", "url.", "gpg.", "sequence.editor", "uploadpack.", "receive.", "protocol.", "http.proxy", "ssh.variant",
}

// stageWritesProtected reports a stage that writes a protected path: by a
// redirection, by a program that writes, by a flag that writes, or by
// configuring git.
func stageWritesProtected(st stage, root string) bool {
	if gitConfiguresRunning(st) {
		return true
	}
	targets := append([]string{}, st.Writes...)
	if stageWrites(st) {
		targets = append(targets, st.Args()...)
	}
	for _, t := range targets {
		if namesProtected(t, root) {
			return true
		}
	}
	return false
}

func stageWrites(st stage) bool {
	if len(st.Writes) > 0 || shellWriters[filepath.Base(st.Name())] || hasWriteFlag(st) {
		return true
	}
	if (st.Name() == "sed" || st.Name() == "perl") && len(st.Args()) > 0 && strings.HasPrefix(st.Args()[0], "-i") {
		return true
	}
	return false
}

func gitConfiguresRunning(st stage) bool {
	if st.Name() != "git" {
		return false
	}
	args := st.Args()
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			key := strings.ToLower(args[i+1])
			for _, k := range gitKeysThatRun {
				if strings.HasPrefix(key, k) {
					return true
				}
			}
		}
		if a == "config" {
			for _, rest := range args[i+1:] {
				for _, r := range gitConfigReads {
					if rest == r {
						return false
					}
				}
			}
			return true
		}
	}
	return false
}

// namesProtected reports a path argument that reaches a protected file, as
// spelled against the home or the root, or resolved under the root.
func namesProtected(arg, root string) bool {
	arg = strings.TrimPrefix(arg, "./")
	for _, p := range protectedInRoot {
		if arg == p || strings.HasPrefix(arg, p+"/") || strings.Contains(arg, "/"+p+"/") || strings.HasSuffix(arg, "/"+p) {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		for _, p := range protectedInHome {
			for _, h := range []string{"~/", "$HOME/", "${HOME}/", home + "/"} {
				if strings.HasPrefix(arg, h+p) {
					return true
				}
			}
		}
	}
	abs := arg
	if strings.HasPrefix(arg, "~/") && err == nil {
		abs = filepath.Join(home, arg[2:])
	} else if !filepath.IsAbs(arg) {
		if root == "" {
			return false
		}
		abs = filepath.Join(root, arg)
	}
	return isProtectedPath(resolveSymlinks(abs), root)
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

func (g *GatedApprover) touchesProtected(req Request) bool {
	for _, prefix := range []string{"write:", "edit:"} {
		if path, ok := strings.CutPrefix(req.Scope, prefix); ok {
			return isProtectedPath(resolveSymlinks(path), g.Root)
		}
	}
	if req.Tool != "bash" {
		return false
	}
	for _, st := range stagesOf(req.Action) {
		if stageWritesProtected(st, g.Root) {
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
