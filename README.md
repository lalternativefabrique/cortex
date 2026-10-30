# Cortex

Go SDK for building agents and running them behind [A2A](https://a2a-protocol.org): a bounded agent loop, a permission layer, MCP tools, memory, and a server that scales from one process to a NATS cluster.

Cortex works with any OpenAI-compatible chat-completions provider. It is the engine under [Cortext](https://cortext.lalter.fr/decouvrir).

```bash
go get github.com/lalternativefabrique/cortex
go get github.com/lalternativefabrique/cortex/connectors
```

## Quick start

Serve an agent and call it over A2A ([`examples/hello`](examples/hello/main.go)):

```go
ctx := context.Background()
provider := agent.Provider{BaseURL: "http://localhost:8000/v1", Model: "m"}

h, err := host.New(ctx, host.Config{
	Agent:    host.Agent{Name: "hello"},
	Provider: provider,
	Token:    "tok",
})
if err != nil {
	log.Fatal(err)
}
defer h.Close()

srv := httptest.NewServer(h.Handler())
defer srv.Close()

text, _, err := client.Say(ctx, client.Agent{URL: srv.URL, Token: "tok"}, client.Request{Text: "hello"})
```

`BaseURL` includes the version segment. Or run the bare loop with no server:

```go
c, err := agent.NewClient(agent.Provider{BaseURL: "http://localhost:8000/v1", Model: "m"})
r, err := agent.NewRunner(agent.Config{Client: c})
res, err := r.Run(ctx, []agent.Message{{Role: agent.RoleUser, Content: "hi"}})
```

## What is in the box

| Package | Role |
| --- | --- |
| `agent` | The loop: limits, compaction, retries, parallel tool calls |
| `host` | One declared agent behind A2A, for a container |
| `client` | Call an agent: `Say` for text, `Ask[T]` for typed output |
| `serve` | The agent on a local machine, for a front end |
| `tools` | File, shell and research tools, approvals, secret handling |
| `sandbox` | Where model-decided commands execute |
| `mcp` | Tools from Model Context Protocol servers |
| `recall`, `memory`, `session` | Memory across conversations, compaction notes, resumable runs |
| `skill`, `skills` | Typed app actions, and Markdown skills invoked by name |
| `connectors` | Separate module: NATS cluster, web, vision, the `cortex` server |

## Documentation

- [The agent loop](docs/loop.md)
- [Security model](docs/security.md)
- [A2A and clustering](docs/a2a.md)
- [Running the `cortex` server](docs/server.md)
- [Memory and skills](docs/memory.md)

## Security defaults

Cortex does not lock itself down for you. With no `Approver`, `host` gates nothing and `serve` allows everything; with no `Sandbox`, `serve` runs commands directly on the host. Read the [security model](docs/security.md) before exposing an agent.

## Status

Pre-1.0: APIs can change between minor versions. `connectors` and the `authz` dependency still resolve some modules from `github.com/lalternative/packages`.

## License

[Business Source License 1.1](LICENSE): source-available, not open source. Production use is allowed except in a competing paid offering, and each version moves to Apache 2.0 four years after publication. See [CONTRIBUTING](CONTRIBUTING.md) and the [CLA](CLA.md).
