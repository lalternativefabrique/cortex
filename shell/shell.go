// Package shell reads a command line the way /bin/sh would, far enough to
// know which programs it runs and which files it writes, and refuses the
// rest.
package shell

import (
	"errors"
	"strings"
	"unicode"
)

// stage is one simple command of a shell line: its words with the quoting
// removed, and the files its redirections write or read.
type Stage struct {
	Words []string
	// quoted marks a word that was quoted or escaped in part.
	Quoted []bool
	// globs marks a word the shell will expand before the command sees it.
	Globs  []bool
	Writes []string
	Reads  []string
}

func (s Stage) String() string { return strings.Join(s.Words, " ") }

func (s Stage) Name() string {
	if len(s.Words) == 0 {
		return ""
	}
	return s.Words[0]
}

// args are the words after the command name.
func (s Stage) Args() []string {
	if len(s.Words) == 0 {
		return nil
	}
	return s.Words[1:]
}

var ErrOpaque = errors.New("the command's effect is not in its words")

// shellKeywords open constructs whose body the words do not show.
var shellKeywords = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "in": true, "select": true, "function": true,
	"time": true, "coproc": true, "[[": true, "]]": true,
}

// assignmentsThatRedirect are variables a leading assignment can use to make
// the command that follows run something else.
var assignmentsThatRedirect = []string{
	"PATH", "LD_", "DYLD_", "IFS", "HOME", "ENV", "BASH_ENV", "PS4", "CDPATH", "SHELLOPTS",
	"PERL5OPT", "PERLLIB", "PYTHON", "NODE_OPTIONS", "RUBYOPT", "JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS",
	"GIT_", "GOFLAGS", "GOTOOLCHAIN", "GOROOT", "GOBIN", "GOPATH", "CARGO_", "RUSTFLAGS", "NPM_CONFIG_",
	"EDITOR", "VISUAL", "PAGER", "LESSOPEN", "LESSCLOSE",
}

// parseShell reads a POSIX shell line into its stages, split on &&, ||, ;,
// |, & and newlines. It fails with ErrOpaque on anything whose effect the
// words do not show: command, process and parameter substitution, heredocs,
// subshells and groups, brace expansion, control-flow keywords, comments, a
// backslash-newline, a quoted or escaped command name, a glob in a command
// name, an assignment that redirects the command, or a syntax error.
func Parse(line string) ([]Stage, error) {
	p := &shellParser{in: []rune(line)}
	if err := p.parse(); err != nil {
		return nil, err
	}
	return p.stages, nil
}

type shellParser struct {
	in     []rune
	pos    int
	stages []Stage
	cur    Stage
	word   strings.Builder
	inWord bool
	quoted bool
	glob   bool
	// trailed marks a line ended by a separator that may end it.
	trailed bool
}

func (p *shellParser) peek(off int) rune {
	if p.pos+off < len(p.in) {
		return p.in[p.pos+off]
	}
	return 0
}

func (p *shellParser) endWord() {
	if !p.inWord {
		return
	}
	p.cur.Words = append(p.cur.Words, p.word.String())
	p.cur.Quoted = append(p.cur.Quoted, p.quoted)
	p.cur.Globs = append(p.cur.Globs, p.glob)
	p.word.Reset()
	p.inWord, p.quoted, p.glob = false, false, false
}

func (p *shellParser) endStage() error {
	p.endWord()
	if len(p.cur.Words) == 0 && len(p.cur.Writes) == 0 && len(p.cur.Reads) == 0 {
		return ErrOpaque
	}
	if err := checkStage(p.cur); err != nil {
		return err
	}
	p.stages = append(p.stages, p.cur)
	p.cur = Stage{}
	return nil
}

func (p *shellParser) parse() error {
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if c != ' ' && c != '\t' {
			p.trailed = false
		}
		switch {
		case c == '\'':
			if err := p.singleQuote(); err != nil {
				return err
			}
		case c == '"':
			if err := p.doubleQuote(); err != nil {
				return err
			}
		case c == '\\':
			next := p.peek(1)
			if next == 0 || next == '\n' {
				return ErrOpaque
			}
			p.inWord, p.quoted = true, true
			p.word.WriteRune(next)
			p.pos += 2
		case c == '$' || c == '`' || c == '(' || c == ')' || c == '{' || c == '}' || c == '\r':
			return ErrOpaque
		case c == '#' && !p.inWord:
			return ErrOpaque
		case c == ' ' || c == '\t':
			p.endWord()
			p.pos++
		case c == '\n' || c == ';':
			if c == ';' && p.peek(1) == ';' {
				return ErrOpaque
			}
			if err := p.separator(1, true); err != nil {
				return err
			}
		case c == '&':
			switch p.peek(1) {
			case '&':
				if err := p.separator(2, false); err != nil {
					return err
				}
			case '>':
				n := 2
				if p.peek(2) == '>' {
					n = 3
				}
				if err := p.redirect(n, true); err != nil {
					return err
				}
			default:
				if err := p.separator(1, true); err != nil {
					return err
				}
			}
		case c == '|':
			n := 1
			if p.peek(1) == '|' || p.peek(1) == '&' {
				n = 2
			}
			if err := p.separator(n, false); err != nil {
				return err
			}
		case c == '>' || c == '<':
			if err := p.redirectAt(c); err != nil {
				return err
			}
		case unicode.IsControl(c):
			return ErrOpaque
		default:
			if c == '*' || c == '?' || c == '[' {
				p.glob = true
			}
			p.inWord = true
			p.word.WriteRune(c)
			p.pos++
		}
	}
	p.endWord()
	if len(p.cur.Words) > 0 || len(p.cur.Writes) > 0 || len(p.cur.Reads) > 0 {
		if err := p.endStage(); err != nil {
			return err
		}
	} else if p.pos > 0 && !p.trailed {
		return ErrOpaque
	}
	if len(p.stages) == 0 {
		return ErrOpaque
	}
	return nil
}

// separator ends a stage. A trailing ; or & ends the line; a trailing pipe
// or && continues it, and an empty stage anywhere is a syntax error the
// shell would refuse too.
func (p *shellParser) separator(width int, mayTrail bool) error {
	p.endWord()
	if len(p.cur.Words) == 0 && len(p.cur.Writes) == 0 && len(p.cur.Reads) == 0 {
		rest := strings.TrimSpace(string(p.in[p.pos+width:]))
		if rest == "" && mayTrail && len(p.stages) > 0 {
			p.pos = len(p.in)
			p.trailed = true
			return nil
		}
		return ErrOpaque
	}
	if err := p.endStage(); err != nil {
		return err
	}
	p.pos += width
	p.trailed = mayTrail
	return nil
}

func (p *shellParser) singleQuote() error {
	p.pos++
	p.inWord, p.quoted = true, true
	for p.pos < len(p.in) {
		if p.in[p.pos] == '\'' {
			p.pos++
			return nil
		}
		p.word.WriteRune(p.in[p.pos])
		p.pos++
	}
	return ErrOpaque
}

func (p *shellParser) doubleQuote() error {
	p.pos++
	p.inWord, p.quoted = true, true
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		switch c {
		case '"':
			p.pos++
			return nil
		case '$', '`':
			return ErrOpaque
		case '\\':
			next := p.peek(1)
			switch next {
			case '"', '\\', '$', '`':
				p.word.WriteRune(next)
				p.pos += 2
			case '\n', 0:
				return ErrOpaque
			default:
				p.word.WriteRune(c)
				p.pos++
			}
		default:
			p.word.WriteRune(c)
			p.pos++
		}
	}
	return ErrOpaque
}

// redirectAt handles > and < with their variants, taking the file
// descriptor from the digits just before them when there are any.
func (p *shellParser) redirectAt(c rune) error {
	if p.inWord {
		if isDigits(p.word.String()) && !p.quoted {
			p.word.Reset()
			p.inWord = false
		} else {
			p.endWord()
		}
	}
	if c == '<' {
		switch p.peek(1) {
		case '<', '(':
			return ErrOpaque
		case '&':
			return p.dup(2)
		}
		return p.redirect(1, false)
	}
	switch p.peek(1) {
	case '(':
		return ErrOpaque
	case '>':
		return p.redirect(2, true)
	case '|':
		return p.redirect(2, true)
	case '&':
		return p.dup(2)
	}
	return p.redirect(1, true)
}

// dup handles >& and <&: a descriptor number or "-" is a duplication, any
// other target is a file bash would open for writing.
func (p *shellParser) dup(width int) error {
	p.pos += width
	target, err := p.target()
	if err != nil {
		return err
	}
	if isDigits(target) || target == "-" {
		return nil
	}
	p.cur.Writes = append(p.cur.Writes, target)
	return nil
}

func (p *shellParser) redirect(width int, write bool) error {
	p.pos += width
	target, err := p.target()
	if err != nil {
		return err
	}
	if write {
		p.cur.Writes = append(p.cur.Writes, target)
	} else {
		p.cur.Reads = append(p.cur.Reads, target)
	}
	return nil
}

// target reads the word a redirection applies to.
func (p *shellParser) target() (string, error) {
	for p.pos < len(p.in) && (p.in[p.pos] == ' ' || p.in[p.pos] == '\t') {
		p.pos++
	}
	var b strings.Builder
	for p.pos < len(p.in) {
		c := p.in[p.pos]
		if c != ' ' && c != '\t' {
			p.trailed = false
		}
		switch {
		case c == '\'':
			start := p.pos + 1
			end := indexRune(p.in, start, '\'')
			if end < 0 {
				return "", ErrOpaque
			}
			b.WriteString(string(p.in[start:end]))
			p.pos = end + 1
		case c == '"':
			start := p.pos + 1
			end := indexRune(p.in, start, '"')
			if end < 0 || strings.ContainsAny(string(p.in[start:end]), "$`\\") {
				return "", ErrOpaque
			}
			b.WriteString(string(p.in[start:end]))
			p.pos = end + 1
		case c == '\\':
			if p.peek(1) == 0 || p.peek(1) == '\n' {
				return "", ErrOpaque
			}
			b.WriteRune(p.peek(1))
			p.pos += 2
		case strings.ContainsRune("$`(){}*?[#", c) || unicode.IsControl(c):
			return "", ErrOpaque
		case strings.ContainsRune(" \t\n;&|<>", c):
			if b.Len() == 0 {
				return "", ErrOpaque
			}
			return b.String(), nil
		default:
			b.WriteRune(c)
			p.pos++
		}
	}
	if b.Len() == 0 {
		return "", ErrOpaque
	}
	return b.String(), nil
}

func indexRune(in []rune, from int, r rune) int {
	for i := from; i < len(in); i++ {
		if in[i] == r {
			return i
		}
	}
	return -1
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// checkStage refuses a stage whose command the words do not name plainly.
func checkStage(s Stage) error {
	for len(s.Words) > 0 && isAssignment(s.Words[0], s.Quoted[0]) {
		name, _, _ := strings.Cut(s.Words[0], "=")
		for _, prefix := range assignmentsThatRedirect {
			if strings.HasPrefix(name, prefix) {
				return ErrOpaque
			}
		}
		s.Words, s.Quoted, s.Globs = s.Words[1:], s.Quoted[1:], s.Globs[1:]
	}
	if len(s.Words) == 0 {
		return nil
	}
	if s.Quoted[0] || s.Globs[0] || shellKeywords[s.Words[0]] || strings.ContainsAny(s.Words[0], " \t") {
		return ErrOpaque
	}
	return nil
}

func isAssignment(word string, quoted bool) bool {
	if quoted {
		return false
	}
	name, _, ok := strings.Cut(word, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		if !(c == '_' || unicode.IsLetter(c) || (i > 0 && unicode.IsDigit(c))) {
			return false
		}
	}
	return true
}

// command is the stage with its leading assignments dropped, which is what
// the rules match.
func (s Stage) Command() Stage {
	for len(s.Words) > 0 && isAssignment(s.Words[0], s.Quoted[0]) {
		s.Words, s.Quoted, s.Globs = s.Words[1:], s.Quoted[1:], s.Globs[1:]
	}
	return s
}
