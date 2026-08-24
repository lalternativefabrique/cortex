package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/sandbox"
)

// BashConfig configures the bash tool.
type BashConfig struct {
	Root    string
	Sandbox sandbox.Sandbox
	// Timeout caps a single command. Zero means DefaultBashTimeout.
	Timeout time.Duration
	// MaxOutputBytes caps each of stdout and stderr in the model-facing
	// result. Zero means DefaultBashOutputBytes.
	MaxOutputBytes int
	Approver       Approver
	// OnLine receives output as it is produced, for live display. The
	// captured output is returned to the model regardless.
	OnLine sandbox.LineFunc
}

const (
	DefaultBashTimeout     = 2 * time.Minute
	DefaultBashOutputBytes = 6000
)

type bashArgs struct {
	Command string `json:"command" jsonschema:"description=Shell command line to run. Pipes, redirections and && are interpreted."`
	Cwd     string `json:"cwd,omitempty" jsonschema:"description=Working directory relative to the workspace root. Defaults to the root."`
	Timeout int    `json:"timeout,omitempty" jsonschema:"description=Timeout in seconds for this command. Raise it for long builds or test suites."`
}

type bashTool struct {
	cfg BashConfig
	// failedOnce records command lines that already exited non-zero in this
	// session. Re-running an unchanged failing command cannot produce a new
	// answer, and a model that retries it will do so until the step budget
	// runs out.
	failedOnce map[string]struct{}
	failedMu   sync.Mutex
}

// NewBash returns a tool that runs shell commands through the sandbox.
func NewBash(cfg BashConfig) agent.Tool {
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultBashTimeout
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = DefaultBashOutputBytes
	}
	if cfg.Approver == nil {
		cfg.Approver = AllowAll{}
	}
	if cfg.Sandbox == nil {
		cfg.Sandbox = sandbox.NewDirect()
	}
	return &bashTool{cfg: cfg, failedOnce: map[string]struct{}{}}
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) Description() string {
	return strings.Join([]string{
		"Run a shell command in the workspace and get back its output and exit code.",
		"",
		"Use this to build, run tests, inspect git state, install dependencies, or run any tooling the task needs.",
		"Do NOT use it to read files (`cat`, `head`, `sed -n`), to search (`grep`, `find`), or to edit files (`sed -i`, `>` redirection, heredocs): the read, grep, glob, edit and write tools are purpose-built for those, return better-structured results, and enforce workspace safety checks that a raw shell command bypasses.",
		"",
		fmt.Sprintf("Runs through /bin/sh, so pipes, redirections, && and variable expansion work. Each call starts in the workspace root unless cwd is given; a `cd` inside one command does not carry over to the next. Commands are killed after %s unless timeout says otherwise.", DefaultBashTimeout),
		"",
		fmt.Sprintf("Returns the exit code, then stderr, then stdout — each capped at about %d bytes with the middle dropped when longer. When that happens the whole output is written to a file and its path named in the result: read or grep that file rather than re-running the command with narrower flags. A non-zero exit is a normal result, not an error: read the output and decide what to do.", DefaultBashOutputBytes),
		"Re-running a command that already failed unchanged is refused; change something or move on.",
		"",
		"This repository is driven by `sklp`. Read its state to diagnose a failure — `sklp deploy logs <service>` for what a service printed, `sklp deploy ls` for what is running, `sklp cache status` for disk. Track work with `sklp issue list|show|new|edit|status`, which is where this project keeps its tickets — it needs --project or --stack unless SKLP_PROJECT is set, and says so. `sklp flow start <name>` opens a branch; `sklp flow end` pushes and opens a pull request, and always asks first whatever the mode, because a PR is public and carries the operator's name.",
		"",
		"The local stack runs under `sklp dev <stack>`, which holds the terminal until stopped — never start one here, it would hold this call until the timeout. `sklp dev down` stops a stack started in another terminal, and `sklp dev --validate` checks the configuration without launching anything. Starting or stopping a stack, running a pipeline and cleaning caches all ask first: it is the operator's working environment, and they may be using it. To find out whether something is up, probe it (`curl -sf http://127.0.0.1:4100/...`) rather than reading the exit code of a kill.",
		"",
		"kubectl, helm and docker are available. Their reading verbs run like any other inspection; the destructive ones are refused in every mode, because a cluster is not the workspace and no git checkout undoes a deleted deployment. Check which context you are pointed at before acting on one.",
		"",
		"Does not return: anything written to files, the state of background processes, or output produced after the timeout. Never start a long-running server here — it will hold the call until it is killed.",
	}, "\n")
}

func (t *bashTool) InputSchema() any { return bashArgs{} }

func (t *bashTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args bashArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return failure("command is required")
	}

	dir, err := resolveWithinRoot(t.cfg.Root, args.Cwd)
	if err != nil {
		return failure("%v", err)
	}

	key := args.Command + "\x00" + dir
	t.failedMu.Lock()
	_, alreadyFailed := t.failedOnce[key]
	t.failedMu.Unlock()
	if alreadyFailed {
		return failure("this exact command already failed in this session; change the command or take a different approach rather than retrying it")
	}

	refused, err := approve(ctx, t.cfg.Approver, Request{
		Tool:   t.Name(),
		Action: args.Command,
		Scope:  "bash:" + commandScope(args.Command),
	})
	if err != nil {
		return failure("approval failed: %v", err)
	}
	if refused != "" {
		return failure("%s: %s", refused, args.Command)
	}

	timeout := t.cfg.Timeout
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout) * time.Second
	}

	out, err := t.cfg.Sandbox.Run(ctx, sandbox.Command{
		Line:    args.Command,
		Dir:     dir,
		Timeout: timeout,
		OnLine:  t.cfg.OnLine,
	})
	if err != nil {
		return failure("%v", err)
	}
	if out.ExitCode != 0 {
		t.failedMu.Lock()
		t.failedOnce[key] = struct{}{}
		t.failedMu.Unlock()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "exit_code=%d\n", out.ExitCode)
	if out.TimedOut {
		fmt.Fprintf(&b, "[killed after %s]\n", timeout)
	}
	// stderr comes first so a failure's cause is not buried under a long
	// stdout when the model triages it.
	if s := strings.TrimSpace(out.Stderr); s != "" {
		fmt.Fprintf(&b, "--- stderr ---\n%s\n", t.fit(s))
	}
	if s := strings.TrimSpace(out.Stdout); s != "" {
		fmt.Fprintf(&b, "--- stdout ---\n%s\n", t.fit(s))
	}
	if out.Stderr == "" && out.Stdout == "" {
		b.WriteString("(no output)\n")
	}

	return agent.ToolResult{
		Content: b.String(),
		Metadata: map[string]any{
			"ok":          out.ExitCode == 0,
			"command":     args.Command,
			"cwd":         dir,
			"exit_code":   out.ExitCode,
			"timed_out":   out.TimedOut,
			"duration_ms": out.Duration.Milliseconds(),
		},
	}, nil
}

// fit shrinks output to what the context can hold, keeping the whole of it on
// disk when it does not fit.
//
// A failing test suite reports its first failure early and its later ones in
// the middle, which is exactly what truncation drops. Naming the file turns
// that loss into a grep the model can run.
func (t *bashTool) fit(s string) string {
	truncated, cut := agent.TruncateMiddle(s, t.cfg.MaxOutputBytes)
	if !cut {
		return truncated
	}
	path, err := spill(s)
	if err != nil {
		// Losing the middle is bad; losing the command's result because the
		// state directory is unwritable would be worse.
		return truncated
	}
	return truncated + "\n" + spillNote(path, len(s))
}

// commandScope keys the approval memory by program name, so approving one
// `go test` covers the next one rather than prompting per invocation.
func commandScope(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return line
	}
	return fields[0]
}
