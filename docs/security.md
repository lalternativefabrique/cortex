# Security model

An agent executes what a model decides, so treat its input as untrusted. Cortex provides layers; you wire them.

## Defaults

| Component | Unset value | Result |
| --- | --- | --- |
| `serve` `Approver` | `tools.AllowAll` | Every tool runs |
| `serve` `Sandbox` | `sandbox.Direct` | Commands run on the host |
| `host` `Approver` | nil | Nothing is gated |
| `host` `Token` | empty | Every caller is refused |

## Approvals

`tools.Approver` returns `Deny`, `Allow` or `AlwaysAllow`. `AllowAll` and `DenyAll` are provided. `Gate()` wraps tools that do not ask for themselves.

`GatedApprover` has four modes (`ask`, `auto`, `yes`, `plan`) and rules such as `bash(git diff *)`. It evaluates in order: protected paths, deny and dangerous, read allowed, must-ask, opaque bash, allow rules, then the mode. With no `Ask` callback, a request needing a person is refused.

Read-only detection is a prefix allowlist, not a shell parser.

`PDPApprover` delegates to an AuthZEN-shaped policy decision point, with resource types `<product>:command`, `:tool` and `:path`. Cortex ships no policy engine.

## Protected paths

Writes are refused to `.ai/settings*.yaml`, `.ai/mcp.yaml`, `.claude`, `.codex`, `.git/hooks` and `.git/config` at the root, and to `.config`, `.ssh`, shell rc files and `.gitconfig` in the home. Symlinks are resolved. Detection inside shell commands only catches the usual spellings, so it is not a boundary.

## Sandboxes

- `sandbox.Direct`: no confinement. It gives a timeout (2 min), process-group kill and an environment allowlist. `Inherit: nil` keeps the whole environment.
- `sandbox.Space`: runs through the external [`sklp`](https://github.com/lalternativefabrique/sklp) binary and fails closed rather than falling back to the host. Isolation is provided by sklp, not by this repository.

Use `Space` or your own `sandbox` implementation for untrusted input.

## Secrets

`SecretGuard` refuses access by file, glob and command before a tool runs. `SecretRedactor` masks environment values with secret-like names, values from workspace secret files, and known shapes (PEM, GitHub, `sk-`, Slack, AKIA, JWT, URL credentials). The runner applies a configured `Redact` to tool results, metadata and errors, and `serve` wires it on A2A turns. Set it yourself when building on `host`.

## MCP

MCP servers are trusted code; the descriptions and schemas they publish reach the model. `ServerConfig.Tools` limits a server to named tools, and nil offers them all. Stdio servers keep `sandbox.DefaultInherit`, plus the names in `Inherit` and the values in `Env`. `LoadConfig` reads a repository's `.ai/mcp.yaml` only when its `trustRepo` argument is true. On `host`, MCP tools are gated only if an `Approver` is set.

Per-turn `mcpHeaders` reach MCP calls and never the model.

## Hooks

`tools.Hook` runs a command through `sh -c` before an approval, with a 30 s timeout. Exit 2 blocks; any other failure does not. It receives `CORTEX_TOOL`, `CORTEX_ACTION`, `CORTEX_ROOT`, and JSON on stdin.

## Inbound authentication

`host` uses a bearer token compared in constant time, or a `Verify` callback. `/health`, the agent card, `GET /skills` and `GET /context` are unauthenticated. The `cortex` server requires `CORTEX_TOKEN` or `CORTEX_ISSUER_URL`, and `CORTEX_CALLERS` restricts JWT clients.

## Network

Cortex has no general SSRF guard of its own. Protection for fetched pages comes from the fetch backend in `connectors/web`. Review what your tools can reach.
