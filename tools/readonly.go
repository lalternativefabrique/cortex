package tools

import (
	"regexp"
	"slices"
	"strings"
)

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
	"which", "whereis", "date", "uname", "echo",
	"git status", "git diff", "git log", "git show", "git branch",
	"git remote -v", "git rev-parse", "git blame", "git describe",
	"go version", "go env", "go list", "go vet", "go doc",
	"npm ls", "pnpm ls", "yarn list",
	"cargo tree", "python --version", "node --version",
	"rg", "grep", "find", "tree", "sed -n", "sort", "uniq", "cut", "basename", "dirname",
}

// writeFlags are options that make an otherwise harmless command write.
// `find` reads until it is given -delete or -exec, and the leading word alone
// cannot tell the two apart.
var writeFlags = []string{"-delete", "-exec", "-execdir", "-ok", "-fprint", "-i", "--in-place"}

// commandWriteFlags are options that write or run something only on the
// command they are listed under: `-o` is harmless to grep and writes for sort.
var commandWriteFlags = map[string][]string{
	"sort":       {"-o", "--output", "--compress-program"},
	"rg":         {"--pre"},
	"git diff":   {"--output", "--ext-diff"},
	"git log":    {"--output", "--ext-diff"},
	"git show":   {"--output", "--ext-diff"},
	"git branch": {"-d", "-D", "-m", "-M", "-c", "-C", "-f", "-u", "--delete", "--move", "--copy", "--force", "--set-upstream-to", "--unset-upstream", "--edit-description"},
	"find":       {"-fprintf", "-fprint0", "-fls", "-okdir"},
}

// sedPrint is the one sed script known to only print: its language can also
// write files and run commands.
var sedPrint = regexp.MustCompile(`^sed -n '?[0-9]+(,[0-9]+)?p'?( [^ ]+)*$`)

func hasWriteFlag(stage string) bool {
	if matchesAnyPrefix(stage, []string{"sed"}) && !sedPrint.MatchString(stage) {
		return true
	}
	fields := strings.Fields(stage)
	for command, flags := range commandWriteFlags {
		if !matchesAnyPrefix(stage, []string{command}) {
			continue
		}
		for _, field := range fields {
			name, _, _ := strings.Cut(field, "=")
			if slices.Contains(flags, name) {
				return true
			}
			if command == "sort" && strings.HasPrefix(field, "-o") && !strings.HasPrefix(field, "--") {
				return true
			}
		}
	}
	return false
}

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
		if !matchesAnyPrefix(stage, readOnlyCommands) || hasWriteFlag(stage) {
			return false
		}
	}
	return true
}

// splitStages breaks a command line on the separators that chain commands.
func splitStages(line string) []string {
	replacer := strings.NewReplacer("&&", "\x00", "||", "\x00", ";", "\x00", "|", "\x00", "\n", "\x00")
	line = replacer.Replace(line)
	// A lone "&" backgrounds a command and starts another; in "2>&1" or
	// "&>file" it is part of a redirection.
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == '&' && (i == 0 || line[i-1] != '>') && (i+1 == len(line) || line[i+1] != '>') {
			b.WriteByte(0)
			continue
		}
		b.WriteByte(c)
	}
	return strings.Split(b.String(), "\x00")
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
