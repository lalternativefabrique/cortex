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
	"git config --get", "git config --list", "git config -l",
	"go version", "go env", "go list", "go vet", "go doc",
	"npm ls", "pnpm ls", "yarn list",
	"cargo tree", "python --version", "node --version",
	"rg", "grep", "find", "tree", "sed -n", "sort", "uniq", "cut", "basename", "dirname",
}

// writeFlags are options that make an otherwise harmless command write or
// run a program, whatever the command: `find` reads until it is given
// -delete or -exec, and the leading word alone cannot tell the two apart.
var writeFlags = []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"}

// commandWriteFlags are options that write or run something only on the
// command they are listed under: `-o` is harmless to grep and writes for sort.
var commandWriteFlags = map[string][]string{
	"sort":       {"-o", "--output", "--compress-program"},
	"tree":       {"-o"},
	"rg":         {"--pre"},
	"date":       {"-s", "--set"},
	"go env":     {"-w", "-u"},
	"go vet":     {"-vettool"},
	"git diff":   {"--output", "--ext-diff"},
	"git log":    {"--output", "--ext-diff"},
	"git show":   {"--output", "--ext-diff"},
	"git branch": {"-d", "-D", "-m", "-M", "-c", "-C", "-f", "-u", "--delete", "--move", "--copy", "--force", "--set-upstream-to", "--unset-upstream", "--edit-description"},
}

// sedPrintScript is the one sed script known to only print: its language
// can also write files and run commands.
var sedPrintScript = regexp.MustCompile(`^[0-9]+(,[0-9]+)?p$`)

// sedPrintFlags are the options a printing sed may take; anything else,
// -i, -e, -f and -s included, is a different program.
var sedPrintFlags = []string{"-n", "-E", "-r", "--quiet", "--silent", "--regexp-extended"}

// execFlags are options that hand a program to run to a command the rules
// otherwise trust.
var execFlags = []string{
	"-exec", "-execdir", "-ok", "-okdir", "--exec", "-toolexec", "-vettool",
	"--pre", "--compress-program", "--ext-diff", "--exec-path",
}

func hasWriteFlag(st stage) bool {
	if st.Name() == "sed" {
		return !sedOnlyPrints(st)
	}
	if st.Name() == "uniq" && len(positionals(st.Args())) > 1 {
		return true
	}
	line := st.String()
	for command, flags := range commandWriteFlags {
		if !matchesAnyPrefix(line, []string{command}) {
			continue
		}
		for _, field := range st.Args() {
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

func sedOnlyPrints(st stage) bool {
	script := ""
	for _, a := range st.Args() {
		switch {
		case slices.Contains(sedPrintFlags, a):
		case strings.HasPrefix(a, "-"):
			return false
		case script == "":
			script = a
		}
	}
	return sedPrintScript.MatchString(script) && slices.Contains(st.Args(), "-n")
}

func positionals(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			out = append(out, a)
		}
	}
	return out
}

// runsProgram reports a stage given a program to run through a flag.
func runsProgram(st stage) bool {
	for _, a := range st.Args() {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(execFlags, name) {
			return true
		}
	}
	return false
}

// isReadOnlyStage reports whether one stage is an inspection command: a
// listed command, with no flag that writes or runs, writing nothing but
// /dev/null.
func isReadOnlyStage(st stage) bool {
	if len(st.Words) == 0 {
		return false
	}
	for _, w := range st.Writes {
		if w != "/dev/null" {
			return false
		}
	}
	for _, a := range st.Args() {
		if slices.Contains(writeFlags, a) {
			return false
		}
	}
	return matchesAnyPrefix(st.String(), readOnlyCommands) && !hasWriteFlag(st) && !runsProgram(st)
}

// isReadOnlyCommand reports whether every stage of a command line is an
// inspection command. A pipeline is only as safe as its most dangerous
// stage, so `ls | tee x` does not qualify, and a line the parser cannot
// read never does.
func isReadOnlyCommand(line string) bool {
	stages, err := parseShell(line)
	if err != nil {
		return false
	}
	for _, st := range stages {
		if !isReadOnlyStage(st.Command()) {
			return false
		}
	}
	return true
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
