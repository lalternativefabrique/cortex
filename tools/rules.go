package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode is the posture an approver takes before consulting any rule.
type Mode string

const (
	// ModeAsk prompts for every action that changes the workspace or runs a
	// command, unless a rule allows it.
	ModeAsk Mode = "ask"
	// ModeAuto additionally lets read-only commands through, so inspecting
	// the repository does not interrupt the operator. Anything that writes
	// still asks.
	ModeAuto Mode = "auto"
	// ModeYes approves everything. The operator has accepted the blast
	// radius up front.
	ModeYes Mode = "yes"
	// ModePlan denies anything that changes the workspace while letting
	// inspection through, so the agent can look but not touch.
	ModePlan Mode = "plan"
)

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeAsk, ModeAuto, ModeYes, ModePlan:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("unknown mode %q; use ask, auto, yes or plan", s)
	}
}

// Rules are the operator's standing decisions, loaded from disk.
//
// Entries are matched against a command line, not a Scope: "git status"
// must be allowed without also allowing "git push", which scope-level
// matching by program name cannot express.
type Rules struct {
	// Allow lists command prefixes that never prompt.
	Allow []string `yaml:"allow"`
	// Deny lists command prefixes that are always refused, even under
	// -mode=yes. Deny wins over Allow.
	Deny []string `yaml:"deny"`
	// AllowWrite lists path globs that may be edited or written without
	// prompting, relative to the workspace root.
	AllowWrite []string `yaml:"allow_write"`
	// DenyWrite lists path globs that may never be written. It wins over
	// AllowWrite and over -mode=yes.
	DenyWrite []string `yaml:"deny_write"`
}

// readOnlyCommands are inspection commands that cannot change the workspace.
// Under ModeAuto they run without prompting.
//
// Entries match on the leading words of a command line, so "git status" is
// listed without "git" being allowed wholesale — the distinction that makes
// the mode useful rather than merely permissive.
var readOnlyCommands = []string{
	"cd", "ls", "pwd", "cat", "head", "tail", "wc", "file", "stat", "du", "df",
	"which", "whereis", "env", "date", "uname", "echo",
	"git status", "git diff", "git log", "git show", "git branch",
	"git remote", "git rev-parse", "git blame", "git describe",
	"go version", "go env", "go list", "go vet", "go doc",
	"npm ls", "pnpm ls", "yarn list",
	"cargo tree", "python --version", "node --version",
	"rg", "grep", "find", "tree", "sed -n", "awk", "sort", "uniq", "cut", "basename", "dirname",
	// Cluster and container inspection. Only the reading verbs are listed:
	// unlike everything else here, these act on something the workspace
	// cannot undo — a deleted deployment is not recoverable by git.
	"kubectl get", "kubectl describe", "kubectl logs", "kubectl top",
	"kubectl explain", "kubectl api-resources", "kubectl config get-contexts",
	"kubectl config current-context", "kubectl version", "kubectl cluster-info",
	"kubectl diff", "kubectl auth can-i",
	"helm list", "helm status", "helm get", "helm history",
	"docker ps", "docker images", "docker logs", "docker inspect",
	// The repository's own runner. Reading its state is how a failure gets
	// diagnosed, and issues are project bookkeeping the agent is expected to
	// keep — both are local and reversible.
	"sklp issue list", "sklp issue show", "sklp issue new", "sklp issue edit",
	"sklp issue status", "sklp issue promote",
	"sklp deploy logs", "sklp deploy ls", "sklp deploy get",
	"sklp cache status", "sklp version", "sklp space ls", "sklp space get",
	"sklp run -resolve-only",
	// start opens a branch or a worktree, which costs a directory and is
	// undone by removing it. end is not here: it pushes.
	"sklp flow start",
	// Checking a stack's configuration launches nothing.
	"sklp dev --validate", "sklp dev -validate", "sklp run --list",
}

// alwaysAsk are commands that run only with the operator's word, whatever
// the mode says. They are not refused — the agent has good reason to reach
// for them — but their effect is outward-facing: a pull request is public
// and carries the operator's name, and waiving approval for the workspace is
// not waiving it for what leaves the machine.
var alwaysAsk = []string{
	"sklp flow end",
	// The local stack is the operator's working environment. Starting one
	// holds a terminal until it is stopped, stopping one takes their running
	// services away, and a pipeline can spend real minutes — none of that
	// should happen while their attention is elsewhere.
	"sklp dev",
	"sklp deploy",
	"sklp space up",
	"sklp space exec",
	"sklp run",
	"sklp clean",
	"git push",
	"gh pr create",
	"gh pr merge",
	"gh release",
	"npm publish",
	"docker push",
}

// dangerousCommands are refused by default in every mode. They are the
// operations whose blast radius is not recoverable from the workspace: a
// force push rewrites shared history, a hard reset discards uncommitted
// work, and rm -rf / needs no explanation.
//
// The list is a backstop, not a security boundary — a determined model can
// express any of these another way. It exists to catch the plausible
// accident, which is what actually happens.
var dangerousCommands = []string{
	"rm -rf /",
	"git push --force",
	"git push -f",
	"git reset --hard",
	"git clean -fdx",
	"shutdown",
	"reboot",
	"mkfs",
	"dd if=",
	// A cluster is not the workspace: nothing here can be undone by a git
	// checkout, and the blast radius is whatever the current context points
	// at — which is rarely what the operator had in mind.
	"kubectl delete",
	"kubectl drain",
	"kubectl cordon",
	"helm uninstall",
	"helm rollback",
	"docker system prune",
	"docker rm -f",
	"sklp clean --deep",
	"sklp clean -v",
	"sklp space down",
	"sklp space purge",
	"sklp deploy delete",
}

// LoadRules reads permissions from the workspace, then the user's config,
// and merges them.
//
// Both files are optional. A repository's rules are additive rather than
// overriding: a repo may widen what it allows for its own tooling, but its
// denials and the user's are both honoured.
func LoadRules(root string) (Rules, error) {
	var merged Rules

	if user, err := userRulesPath(); err == nil {
		r, err := readRules(user)
		if err != nil {
			return merged, err
		}
		merged.merge(r)
	}

	repo, err := readRules(filepath.Join(root, ".ai", "permissions.yaml"))
	if err != nil {
		return merged, err
	}
	merged.merge(repo)
	return merged, nil
}

func userRulesPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "skode", "permissions.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "skode", "permissions.yaml"), nil
}

func readRules(path string) (Rules, error) {
	var r Rules
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("parse %s: %w", path, err)
	}
	return r, nil
}

func (r *Rules) merge(other Rules) {
	r.Allow = append(r.Allow, other.Allow...)
	r.Deny = append(r.Deny, other.Deny...)
	r.AllowWrite = append(r.AllowWrite, other.AllowWrite...)
	r.DenyWrite = append(r.DenyWrite, other.DenyWrite...)
}

// GatedApprover applies mode and rules before falling back to an operator
// prompt.
type GatedApprover struct {
	Mode  Mode
	Rules Rules
	// Ask is consulted when neither the mode nor a rule settles the request.
	// Nil means deny, which is the safe reading of "nobody can answer".
	Ask Approver
	// Root anchors write-path globs.
	Root string
}

// Approve decides a request.
//
// Tools of one model turn run concurrently, so the reason for a denial
// travels with the decision rather than being stashed on the approver.
func (g *GatedApprover) Approve(ctx context.Context, req Request) (Decision, error) {
	if g.isDenied(req) {
		return Deny, RefusedByRule
	}
	// Before the allow rules, not after: a broad allow like "sklp" would
	// otherwise let a push through, which is the one thing the operator was
	// asked about.
	if g.mustAsk(req) {
		if g.Ask == nil {
			return Deny, RefusedUnattended
		}
		return g.Ask.Approve(ctx, req)
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
	if g.Ask == nil {
		return Deny, RefusedUnattended
	}
	return g.Ask.Approve(ctx, req)
}

// mustAsk reports whether a request needs the operator's word regardless of
// the mode.
func (g *GatedApprover) mustAsk(req Request) bool {
	if req.Tool != "bash" {
		return false
	}
	line := normalizeCommand(req.Action)
	// The longest match wins: "sklp deploy" asks, but "sklp deploy logs" is
	// an inspection, and a prefix rule alone would prompt for every log tail.
	ask := longestPrefix(line, alwaysAsk)
	return ask != "" && len(ask) >= len(longestPrefix(line, readOnlyCommands))
}

// longestPrefix returns the longest entry that the line begins with, or empty
// when none does.
func longestPrefix(line string, prefixes []string) string {
	best := ""
	for _, p := range prefixes {
		if len(p) > len(best) && matchesAnyPrefix(line, []string{p}) {
			best = p
		}
	}
	return best
}

func (g *GatedApprover) isDenied(req Request) bool {
	if req.Tool == "bash" {
		line := normalizeCommand(req.Action)
		return matchesAnyPrefix(line, dangerousCommands) || matchesAnyPrefix(line, g.Rules.Deny)
	}
	return matchesAnyGlob(g.relative(req), g.Rules.DenyWrite)
}

func (g *GatedApprover) isAllowed(req Request) bool {
	if req.Tool == "bash" {
		line := normalizeCommand(req.Action)
		if matchesAnyPrefix(line, g.Rules.Allow) {
			return true
		}
		// Both auto and plan let an inspection command through. Refusing one
		// in plan mode costs a step and teaches the model nothing: it wanted
		// to look, which is the whole point of the mode.
		switch g.Mode {
		case ModeAuto, ModePlan:
			return isReadOnlyCommand(line)
		}
		return false
	}
	// A write is never allowed in plan mode, whatever the globs say.
	if g.Mode == ModePlan {
		return false
	}
	return matchesAnyGlob(g.relative(req), g.Rules.AllowWrite)
}

// relative renders the target path of a write request relative to the root,
// which is the form write globs are written against.
func (g *GatedApprover) relative(req Request) string {
	path := strings.TrimPrefix(req.Scope, "edit:")
	path = strings.TrimPrefix(path, "write:")
	if g.Root == "" {
		return path
	}
	return relativeToRoot(g.Root, path)
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

func matchesAnyGlob(path string, globs []string) bool {
	for _, g := range globs {
		if g == "" {
			continue
		}
		if matchGlob(g, path) {
			return true
		}
	}
	return false
}

// normalizeCommand collapses whitespace so rules written naturally still
// match a command the model formatted differently.
func normalizeCommand(line string) string {
	return strings.Join(strings.Fields(line), " ")
}
