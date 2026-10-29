package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Mode is the posture an approver takes before consulting any rule.
type Mode string

const (
	// ModeAsk prompts for every action that changes the workspace or runs a
	// command, unless a rule settles it.
	ModeAsk Mode = "ask"
	// ModeAuto additionally lets read-only commands through, so inspecting
	// the repository does not interrupt the operator. Anything that writes
	// still asks.
	ModeAuto Mode = "auto"
	// ModeYes approves everything a rule does not forbid. The operator has
	// accepted the blast radius up front.
	ModeYes Mode = "yes"
	// ModePlan denies anything that changes the workspace while letting
	// inspection through: the agent can look but not touch.
	ModePlan Mode = "plan"
)

func ParseMode(s string) (Mode, error) {
	switch Mode(strings.TrimSpace(s)) {
	case "":
		return ModeAsk, nil
	case ModeAsk, ModeAuto, ModeYes, ModePlan:
		return Mode(strings.TrimSpace(s)), nil
	default:
		return "", fmt.Errorf("unknown mode %q; use ask, auto, yes or plan", s)
	}
}

// Rule names a tool and, optionally, what it is about to do: "bash(git diff *)",
// "write(./tmp/**)", "read(~/.zshrc)", "fetch_url". The form is the one
// Claude Code settings use, lowercased to cortex's tool names.
//
// For bash the pattern is a command line: a trailing " *" matches every
// command starting with those words on a word boundary, "*" matches any
// command, anything else matches that exact command. For read, edit and
// write it is a path glob, relative to the workspace root ("./" or nothing),
// or under the home directory ("~/"). For any other tool an empty pattern
// or "*" matches every call, and anything else is a glob over the call's
// rendered arguments.
type Rule struct {
	Tool    string
	Pattern string
}

func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	open := strings.IndexByte(s, '(')
	if open < 0 {
		if !isToolName(s) {
			return Rule{}, fmt.Errorf("rule %q: expected tool or tool(pattern)", s)
		}
		return Rule{Tool: s}, nil
	}
	if !strings.HasSuffix(s, ")") {
		return Rule{}, fmt.Errorf("rule %q: missing closing parenthesis", s)
	}
	tool := s[:open]
	if !isToolName(tool) {
		return Rule{}, fmt.Errorf("rule %q: %q is not a tool name", s, tool)
	}
	pattern := strings.TrimSpace(s[open+1 : len(s)-1])
	if pattern == "" {
		return Rule{}, fmt.Errorf("rule %q: empty pattern; write %s to match every call", s, tool)
	}
	return Rule{Tool: tool, Pattern: pattern}, nil
}

func ParseRules(lines []string) ([]Rule, error) {
	out := make([]Rule, 0, len(lines))
	for _, l := range lines {
		r, err := ParseRule(l)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func isToolName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// Matches reports whether the rule covers the request. root anchors path
// patterns; every rule is read as if written against it.
func (r Rule) Matches(req Request, root string) bool {
	if r.Tool != req.Tool {
		return false
	}
	switch req.Tool {
	case "bash":
		return matchesCommand(normalizeCommand(req.Action), r.Pattern)
	case "read", "edit", "write":
		return matchesPath(targetPath(req), root, r.Pattern)
	default:
		if r.Pattern == "" || r.Pattern == "*" {
			return true
		}
		return matchGlob(r.Pattern, req.Action)
	}
}

// matchesAnyStage is Matches for a deny: a chained command is refused when
// any of its stages is, since "ls && git push" pushes.
func (r Rule) matchesAnyStage(req Request, root string) bool {
	if r.Tool != "bash" || req.Tool != "bash" {
		return r.Matches(req, root)
	}
	for _, stage := range splitStages(req.Action) {
		if stage = normalizeCommand(stage); stage != "" && matchesCommand(stage, r.Pattern) {
			return true
		}
	}
	return false
}

func matchesCommand(line, pattern string) bool {
	switch {
	case pattern == "" || pattern == "*":
		return true
	case strings.HasSuffix(pattern, " *"):
		return matchesAnyPrefix(line, []string{normalizeCommand(strings.TrimSuffix(pattern, " *"))})
	default:
		return line == normalizeCommand(pattern)
	}
}

func matchesPath(abs, root, pattern string) bool {
	switch {
	case pattern == "" || pattern == "*" || pattern == "**":
		return true
	case strings.HasPrefix(pattern, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		return matchGlob(filepath.Join(home, pattern[2:]), abs)
	case filepath.IsAbs(pattern):
		return matchGlob(pattern, abs)
	}
	pattern = strings.TrimPrefix(pattern, "./")
	rel := abs
	if root != "" {
		rel = relativeToRoot(root, abs)
	}
	return matchGlob(pattern, rel)
}

// targetPath is the file a read, edit or write is about, as the tool scoped
// it.
func targetPath(req Request) string {
	return strings.TrimPrefix(req.Scope, req.Tool+":")
}

func normalizeCommand(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// Rules are the standing decisions a run starts with. Deny wins over ask,
// ask over allow, and all three over the mode.
type Rules struct {
	Allow []Rule
	Ask   []Rule
	Deny  []Rule
}

// DefaultDangerous are the commands refused in every mode, whatever the
// rules say: their effect is not recoverable from the workspace. The list is
// a backstop against the plausible accident, not a security boundary.
var DefaultDangerous = []string{
	"rm -rf /",
	"git push --force",
	"git push -f",
	"git reset --hard",
	"git clean -fdx",
	"shutdown",
	"reboot",
	"mkfs",
	"dd if=",
}

// DefaultAlwaysAsk are the commands that run only with the operator's word,
// whatever the mode: their effect is outward-facing and carries the
// operator's name. A product adds its own, such as its deploy verbs.
var DefaultAlwaysAsk = []string{
	"git push",
	"gh pr create",
	"gh pr merge",
	"gh release",
	"npm publish",
	"docker push",
}

// GatedApprover applies the mode and the rules before falling back to an
// operator prompt. It is what a workstation runs: the person asking is the
// person whose machine it is, and the rules are theirs.
type GatedApprover struct {
	Mode  Mode
	Rules Rules
	// Dangerous and AlwaysAsk are command prefixes. Nil takes the defaults;
	// an empty, non-nil slice takes none.
	Dangerous []string
	AlwaysAsk []string
	// Ask is consulted when neither the mode nor a rule settles the request.
	// Nil means deny: nobody can answer.
	Ask Approver
	// Root anchors path patterns.
	Root string
}

func (g *GatedApprover) dangerous() []string {
	if g.Dangerous == nil {
		return DefaultDangerous
	}
	return g.Dangerous
}

func (g *GatedApprover) alwaysAsk() []string {
	if g.AlwaysAsk == nil {
		return DefaultAlwaysAsk
	}
	return g.AlwaysAsk
}

// Approve decides a request. Tools of one model turn run concurrently, so
// the reason for a denial travels with the decision.
func (g *GatedApprover) Approve(ctx context.Context, req Request) (Decision, error) {
	if g.touchesProtected(req) {
		return Deny, RefusedProtected
	}
	if g.isDenied(req) {
		return Deny, RefusedByRule
	}
	// A read is never prompted: a deny rule is the only thing that stops one.
	if req.Tool == "read" {
		return Allow, nil
	}
	// Before the allow rules, not after: a broad allow like "sklp" would
	// otherwise let a push through, which is the one thing the operator was
	// asked about.
	if g.mustAsk(req) {
		return g.ask(ctx, req)
	}
	if g.isAllowed(req) {
		return Allow, nil
	}
	switch g.Mode {
	case ModeYes:
		return Allow, nil
	case ModePlan:
		return Deny, RefusedByMode
	}
	return g.ask(ctx, req)
}

func (g *GatedApprover) ask(ctx context.Context, req Request) (Decision, error) {
	if g.Ask == nil {
		return Deny, RefusedUnattended
	}
	return g.Ask.Approve(ctx, req)
}

func (g *GatedApprover) isDenied(req Request) bool {
	if req.Tool == "bash" {
		for _, stage := range splitStages(req.Action) {
			if stage = normalizeCommand(stage); stage != "" && matchesAnyPrefix(stage, g.dangerous()) {
				return true
			}
		}
	}
	for _, r := range g.Rules.Deny {
		if r.matchesAnyStage(req, g.Root) {
			return true
		}
	}
	return false
}

func (g *GatedApprover) mustAsk(req Request) bool {
	for _, r := range g.Rules.Ask {
		if r.matchesAnyStage(req, g.Root) {
			return true
		}
	}
	if req.Tool != "bash" {
		return false
	}
	line := normalizeCommand(req.Action)
	// The longest match wins: "sklp deploy" asks, but "sklp deploy logs" is
	// an inspection, and a prefix rule alone would prompt for every log tail.
	ask := longestPrefix(line, g.alwaysAsk())
	return ask != "" && len(ask) >= len(longestPrefix(line, readOnlyCommands))
}

func longestPrefix(line string, prefixes []string) string {
	best := ""
	for _, p := range prefixes {
		if len(p) > len(best) && matchesAnyPrefix(line, []string{p}) {
			best = p
		}
	}
	return best
}

func (g *GatedApprover) isAllowed(req Request) bool {
	// A write is never allowed in plan mode, whatever the rules say.
	if g.Mode == ModePlan && req.Tool != "bash" {
		return false
	}
	for _, r := range g.Rules.Allow {
		if r.Matches(req, g.Root) {
			return true
		}
	}
	if req.Tool != "bash" {
		return false
	}
	// Both auto and plan let an inspection command through. Refusing one in
	// plan mode costs a step and teaches the model nothing: it wanted to
	// look, which is the whole point of the mode.
	switch g.Mode {
	case ModeAuto, ModePlan:
		return isReadOnlyCommand(normalizeCommand(req.Action))
	}
	return false
}
