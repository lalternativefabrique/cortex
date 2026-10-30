package tools

import "strings"

// interpreters run code given on their command line or read from elsewhere,
// so what they do is not in the text a rule matches.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"eval": true, "exec": true, "source": true, ".": true, "xargs": true,
	"python": true, "python3": true, "node": true, "perl": true, "ruby": true, "php": true, "deno": true, "bun": true,
}

// isOpaque reports a command whose effect a rule on its text cannot see: a
// substitution, an interpreter given code, or a command name spelled to
// dodge a match ("r\m", "'rm'"). Such a command is never allowed by a rule;
// it goes to whoever answers questions.
func isOpaque(line string) bool {
	for _, marker := range []string{"$(", "`", "<(", ">(", "${"} {
		if strings.Contains(line, marker) {
			return true
		}
	}
	for _, stage := range splitStages(line) {
		fields := strings.Fields(stage)
		for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.ContainsAny(fields[0], `\'"`) {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if strings.ContainsAny(name, `\'"*?[`) {
			return true
		}
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if interpreters[name] {
			return true
		}
	}
	return false
}
