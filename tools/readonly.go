package tools

import "strings"

// readOnlyCommands are inspection commands that cannot change the workspace.
//
// Entries match on the leading words of a command line, so "git status" is
// listed without "git" being allowed wholesale — the distinction that makes
// the list useful rather than merely permissive.
//
// The list is universal on purpose: it holds nothing a particular product
// ships. Which of its own verbs a product lets through is a policy decision,
// and it belongs in that product's rego, not in the kernel.
var readOnlyCommands = []string{
	"cd", "ls", "pwd", "cat", "head", "tail", "wc", "file", "stat", "du", "df",
	"which", "whereis", "env", "date", "uname", "echo",
	"git status", "git diff", "git log", "git show", "git branch",
	"git remote", "git rev-parse", "git blame", "git describe",
	"go version", "go env", "go list", "go vet", "go doc",
	"npm ls", "pnpm ls", "yarn list",
	"cargo tree", "python --version", "node --version",
	"rg", "grep", "find", "tree", "sed -n", "awk", "sort", "uniq", "cut", "basename", "dirname",
}

// writeFlags are options that make an otherwise harmless command write.
// `find` reads until it is given -delete or -exec, and the leading word alone
// cannot tell the two apart.
var writeFlags = []string{"-delete", "-exec", "-execdir", "-ok", "-fprint", "-i", "--in-place"}

// isReadOnlyCommand reports whether every stage of a command line is an
// inspection command.
//
// A pipeline is only as safe as its most dangerous stage, so `ls | tee x`
// does not qualify. Redirections disqualify outright: they write regardless
// of what produced the bytes.
func isReadOnlyCommand(line string) bool {
	if strings.ContainsAny(line, ">") {
		return false
	}
	for _, f := range strings.Fields(line) {
		for _, w := range writeFlags {
			if f == w {
				return false
			}
		}
	}
	for _, stage := range splitStages(line) {
		stage = strings.TrimSpace(stage)
		if stage == "" {
			continue
		}
		if !matchesAnyPrefix(stage, readOnlyCommands) {
			return false
		}
	}
	return true
}

// splitStages breaks a command line on the separators that chain commands.
func splitStages(line string) []string {
	replacer := strings.NewReplacer("&&", "\x00", "||", "\x00", ";", "\x00", "|", "\x00")
	return strings.Split(replacer.Replace(line), "\x00")
}

// matchesAnyPrefix reports whether the line starts with one of the patterns,
// on a word boundary so "gitk" is not matched by "git".
func matchesAnyPrefix(line string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if line == p {
			return true
		}
		if strings.HasPrefix(line, p) && isBoundary(line[len(p)]) {
			return true
		}
	}
	return false
}

func isBoundary(c byte) bool {
	return c == ' ' || c == '\t'
}
