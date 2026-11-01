package tools

import "strings"

// interpreters run code given on their command line or read from elsewhere,
// or run another program named in their arguments, so what they do is not
// in the text a rule matches.
var interpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"eval": true, "exec": true, "source": true, ".": true, "xargs": true,
	"python": true, "python3": true, "node": true, "perl": true, "ruby": true, "php": true, "deno": true, "bun": true,
	"env": true, "nohup": true, "nice": true, "ionice": true, "timeout": true, "setsid": true, "stdbuf": true,
	"sudo": true, "doas": true, "su": true, "command": true, "builtin": true, "watch": true,
	"strace": true, "ltrace": true, "chroot": true, "unshare": true, "nsenter": true, "flock": true, "script": true,
}

// isOpaque reports a command whose effect a rule on its text cannot see: a
// substitution, an interpreter given code, a wrapper given a program, or a
// command name spelled to dodge a match ("r\m", "'rm'"). Such a command is
// never allowed by a rule; it goes to whoever answers questions.
func isOpaque(line string) bool {
	stages, err := parseShell(line)
	if err != nil {
		return true
	}
	for _, st := range stages {
		name := st.Command().Name()
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if interpreters[name] {
			return true
		}
	}
	return false
}
