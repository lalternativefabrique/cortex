# Cortex

Go SDK for building agents and running them behind [A2A](https://a2a-protocol.org): a bounded agent loop, a permission layer, MCP tools, memory, and a server that scales from one process to a NATS cluster.

Cortex works with any OpenAI-compatible chat-completions provider. It is the engine under [Cortext](https://cortext.lalter.fr/decouvrir).

```bash
go get github.com/lalternativefabrique/cortex
go get github.com/lalternativefabrique/cortex/connectors
```

## Quick start

Serve an agent over A2A ([`examples/hello`](examples/hello/main.go)). The process takes its secrets from the environment, bounds each turn, and drains on `SIGTERM`:

```go
ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

token := os.Getenv("CORTEX_TOKEN")
if token == "" {
	log.Fatal("CORTEX_TOKEN is required")
}

h, err := host.New(ctx, host.Config{
	Agent: host.Agent{Name: "hello", Instructions: "Answer briefly."},
	Provider: agent.Provider{
		BaseURL: os.Getenv("CORTEX_BASE_URL"),
		APIKey:  os.Getenv("CORTEX_API_KEY"),
		Model:   os.Getenv("CORTEX_MODEL"),
		Timeout: 2 * time.Minute,
	},
	Token:          token,
	PublicURL:      os.Getenv("CORTEX_PUBLIC_URL"),
	MaxSteps:       10,
	MaxInputTokens: 200_000,
})
if err != nil {
	log.Fatal(err)
}
defer h.Close()

srv := &http.Server{Addr: ":7400", Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
go func() {
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}()
if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
	log.Fatal(err)
}
```

Run it, then call it from any Go service:

```bash
export CORTEX_TOKEN=$(openssl rand -hex 32)
CORTEX_BASE_URL=https://api.openai.com/v1 CORTEX_API_KEY=... CORTEX_MODEL=gpt-5 \
go run ./examples/hello
```

```go
text, _, err := client.Say(ctx,
	client.Agent{URL: "http://localhost:7400", Token: os.Getenv("CORTEX_TOKEN")},
	client.Request{Text: "hello"})
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

## Before production

The quick start is one instance with a shared token and no tools. Each step below is a choice it leaves open:

| Need | Set |
| --- | --- |
| Identify callers, not just share a token | `host.Config.Verify` |
| Control what the model may do with tools | `host.Config.Approver`; every tool runs ungated without it |
| Keep conversations across restarts, or run several instances | `Turns` and `History` from `connectors/cluster` (NATS); the default is in memory, for one instance |
| Isolate model-decided commands | `sandbox.Space`, the workspace's [sklp](https://github.com/lalternativefabrique/sklp) agent space, is the default; `sandbox.Container` is for a service that is itself deployed confined |
| Terminate TLS | A reverse proxy, or your own `http.Server` configuration |

To skip the wiring of identity and NATS, the [`cortex` server](docs/server.md) takes them from environment variables.

## Documentation

- [The agent loop](docs/loop.md)
- [Security model](docs/security.md)
- [A2A and clustering](docs/a2a.md)
- [Running the `cortex` server](docs/server.md)
- [Memory and skills](docs/memory.md)

## Security defaults

With no `Approver`, `host` gates nothing, and `serve` lets reads and inspection commands through and refuses the rest. Commands only ever run inside an sklp agent space: without sklp they fail, they never fall back to the host. Read the [security model](docs/security.md) before exposing an agent.

## Status

Pre-1.0: APIs can change between minor versions. `connectors` and the `authz` dependency still resolve some modules from `github.com/lalternative/packages`.

## License

[Business Source License 1.1](LICENSE): source-available, not open source. Production use is allowed except in a competing paid offering, and each version moves to Apache 2.0 four years after publication. See [CONTRIBUTING](CONTRIBUTING.md) and the [CLA](CLA.md).
