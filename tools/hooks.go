package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// Hook is a command the operator runs before a tool acts: a script in the
// repository that can refuse the call. Matcher names the tool, or "*".
type Hook struct {
	Matcher string `yaml:"matcher" json:"matcher"`
	Command string `yaml:"command" json:"command"`
}

// HookExitBlocks is the exit status a hook uses to refuse the action; what
// it printed is given to the model as the reason. Any other failure is the
// hook's own problem and does not block.
const HookExitBlocks = 2

const hookTimeout = 30 * time.Second

// WithHooks runs the matching hooks before asking next. A hook that blocks
// ends the decision there.
func WithHooks(next Approver, hooks []Hook, root string) Approver {
	if len(hooks) == 0 {
		return next
	}
	return &hooked{next: next, hooks: hooks, root: root}
}

type hooked struct {
	next  Approver
	hooks []Hook
	root  string
}

func (h *hooked) Approve(ctx context.Context, req Request) (Decision, error) {
	for _, hook := range h.hooks {
		if !hookMatches(hook.Matcher, req.Tool) {
			continue
		}
		if reason, blocked := h.run(ctx, hook, req); blocked {
			return Deny, Refusal("a hook refused this action: " + reason)
		}
	}
	return h.next.Approve(ctx, req)
}

func hookMatches(matcher, tool string) bool {
	matcher = strings.TrimSpace(matcher)
	return matcher == "" || matcher == "*" || matcher == tool || matchGlob(matcher, tool)
}

func (h *hooked) run(ctx context.Context, hook Hook, req Request) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", hook.Command)
	cmd.Dir = h.root
	cmd.Env = append(cmd.Environ(),
		"CORTEX_TOOL="+req.Tool,
		"CORTEX_ACTION="+req.Action,
		"CORTEX_ROOT="+h.root,
	)
	input, _ := json.Marshal(map[string]string{"tool": req.Tool, "action": req.Action, "scope": req.Scope})
	cmd.Stdin = bytes.NewReader(input)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == HookExitBlocks {
		reason := strings.TrimSpace(out.String())
		if reason == "" {
			reason = hook.Command
		}
		return reason, true
	}
	return "", false
}
