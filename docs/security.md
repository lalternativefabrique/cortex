# Security model

An agent executes what a model decides, so treat its input as untrusted. Cortex provides layers; you wire them.

## Defaults

| Component | Unset value | Result |
| --- | --- | --- |
| `serve` `Approver` | `GatedApprover` in `auto` mode with nobody to ask | Reads and inspection commands run, the rest is refused |
| `serve` `Sandbox` | `sandbox.Space` | Commands run in the sklp agent space, or fail |
| `serve` `Token` | generated | `Server.Token()` returns it for the window |
| `host` `Approver` | nil | Nothing is gated |
| `host` `Token` | empty | Every caller is refused |

## Approvals

`tools.Approver` returns `Deny`, `Allow` or `AlwaysAllow`. `AllowAll` and `DenyAll` are provided. `Gate()` wraps tools that do not ask for themselves.

`GatedApprover` has four modes (`ask`, `auto`, `yes`, `plan`) and rules such as `bash(git diff *)`. It evaluates in order: protected paths, deny and dangerous, read allowed, must-ask, opaque bash, allow rules, then the mode. With no `Ask` callback, a request needing a person is refused.

Command lines are read by the `shell` package: quotes and escapes resolved, stages split on `&&`, `||`, `;`, `|` and `&`, redirections kept as the files they write or read. Every list — read-only, dangerous, always-ask, allow, deny, `Commands` — is matched against each stage's plain words, so a quoted flag or a chained command is seen for what it is. What the parser cannot read is opaque: substitutions, heredocs, subshells, control flow, a quoted command name, an assignment that redirects the command. An opaque line is never allowed by a rule; it goes to `Ask` or is refused. Read-only commands are still a list; a listed command is disqualified by a flag that writes or runs a program, by a second output file (`uniq`, `sort -o`, `tree -o`), and `sed` passes only as `sed -n <lines>p`.

`GatedApprover.Commands` names the programs that may run: when non-nil, only the listed prefixes run, in every mode, each stage is checked, an opaque command is refused, and so is a stage handing a program to a listed one (`find -exec`, `go test -exec`, `-toolexec`, `rg --pre`). It bounds program names, not what those programs do. `Dangerous` is a backstop against accidents, not a boundary. The sandbox is the boundary.

`PDPApprover` delegates to an AuthZEN-shaped policy decision point, with resource types `<product>:command`, `:tool` and `:path`. Cortex ships no policy engine.

## Protected paths

Writes are refused to `.ai/settings*.yaml`, `.ai/mcp.yaml`, `.claude`, `.codex`, `.git/hooks` and `.git/config` at the root, and to `.config`, `.ssh`, shell rc files and `.gitconfig` in the home. Symlinks are resolved. In a shell stage, a redirection, a writing program or a writing flag naming such a path is refused, and so is `git config` other than its reads and `git -c` with a key that runs something (`core.hooksPath`, `core.sshCommand`, `alias.`, ...). Inside the sklp space these paths are also mounted read-only, which is the part that holds.

## Sandboxes

- `sandbox.Space`: runs through the external [`sklp`](https://github.com/lalternativefabrique/sklp) binary (`sklp agent up`) and fails closed rather than falling back to the host. Isolation is provided by sklp, not by this repository. The agent space is a rootless runc container with its own user and network namespaces, sklp's default seccomp profile, `no_new_privileges`, a reduced capability set, and masked and read-only system paths. Only the workspace is mounted, at `/workspace`; there is no home directory, no host process and no host loopback. Files named like secrets (`.env*`, `.netrc`, `.pem`, `.key`, `id_rsa`, ...) and the paths in `Hidden` are mounted empty; `.ai`, `.claude`, `.codex`, `.sklp`, `.git/hooks` and `.git/config` are mounted read-only, so the agent cannot rewrite its own rules through the shell. The environment holds the recipe's toolchain and nothing from the host. Egress is open: the namespace has outbound network through slirp4netns and no domain allowlist. Linux amd64 only.

- `sandbox.Container`: this process's own container, with a timeout, process-group kill and the environment allowlist, and nothing more. It is for a service deployed confined, such as a core running its agents' tasks, where the container is the boundary. It is never a default: `NewContainer` returns `ErrNotConfined` outside a container unless `CORTEX_CONFINED=1` says the host is the boundary.

Nothing runs a command on a person's machine. `Space` is the default everywhere a command can run; a `Sandbox` of your own replaces it.

## Secrets

`SecretGuard` refuses access by file, glob and command before a tool runs: a path named as a word or a `<` redirection, and a glob or bracket pattern expanded against `Root` and the home the way the shell would. The `grep` tool asks its `Approver` with the file it searches, so a secret file named as its path is refused too; a search across a tree still reaches what the globs do not exclude, and the redactor is the backstop there. `SecretRedactor` masks environment values with secret-like names, values from workspace secret files, and known shapes (PEM, GitHub, `sk-`, Slack, AKIA, JWT, URL credentials). The runner applies a configured `Redact` to tool results, metadata and errors, and `serve` wires it on A2A turns. Set it yourself when building on `host`.

## MCP

MCP servers are trusted code; the descriptions and schemas they publish reach the model. `ServerConfig.Tools` limits a server to named tools, and nil offers them all. Stdio servers keep `sandbox.DefaultInherit`, plus the names in `Inherit` and the values in `Env`. `LoadConfig` reads a repository's `.ai/mcp.yaml` only when its `trustRepo` argument is true. On `host`, MCP tools are gated only if an `Approver` is set.

Per-turn `mcpHeaders` reach MCP calls and never the model. A header given as `{"name": "value"}` goes to every HTTP server of the turn; given as `{"server": {"name": "value"}}` it goes to that server only. `Content-Type` cannot be overridden. A response is read up to 32 MiB.

## Hooks

`tools.Hook` runs a command through `sh -c` before an approval, with a 30 s timeout. Exit 2 blocks; any other failure does not. It receives `CORTEX_TOOL`, `CORTEX_ACTION`, `CORTEX_ROOT`, and JSON on stdin.

## Inbound authentication

`host` uses a bearer token compared in constant time, or a `Verify` callback, and reads at most `MaxRequestBytes` (64 MiB) of a request. A turn sends at most `MaxInputTokens` to the model, `DefaultMaxInputTokens` when unset. In-memory history keeps 2000 conversations of at most 1000 messages. `serve` generates its token when given none and compares it in constant time. `/health`, the agent card, `GET /skills` and `GET /context` are unauthenticated. The `cortex` server requires `CORTEX_TOKEN` or `CORTEX_ISSUER_URL`, and `CORTEX_CALLERS` restricts JWT clients.

## Network

Cortex has no general SSRF guard of its own. Protection for fetched pages comes from the fetch backend in `connectors/web`. Review what your tools can reach.
