# A2A and clustering

## Serving

`host.New` requires `Agent.Name` and `Provider.BaseURL`. `Host.Handler()` exposes:

| Route | Purpose |
| --- | --- |
| `POST /a2a` | A2A JSON-RPC |
| `GET /.well-known/agent.json` | Agent card: streaming, bearer security, the Cortex extension |
| `GET /health` | Health |
| `GET /skills`, `POST /skills/refresh` | Skill catalog |
| `GET /context` | Agent context |

Message metadata carries per-turn options: `subject`, `mcpHeaders`, `turnContext`, `turnToken`, `outputSchema`, `skill`, `model`, `reasoningEffort`, `recall`. `mcpHeaders` is either headers for every server (`{"Authorization": "..."}`) or headers by server name (`{"orbit": {"Authorization": "..."}}`).

`serve` is a separate local server with A2A at `/a2a`, where the context ID is the session, plus `/turn`, `/turn/steer`, `/sessions` and `/diff`.

## Calling

```go
text, turn, err := client.Say(ctx, agent, client.Request{Text: "hello"})
out, turn, err := client.Ask[Summary](ctx, agent, client.Request{Text: "summarise"})
```

`Ask` sends a schema reflected from `T` and decodes the answer. A text answer is sent back for correction, and a turn that keeps answering in text fails.

## One process

`host.NewLocalTurns` runs turns in process: concurrency 8 and a 2 minute queue wait by default. Turns of one conversation run one after the other, and a turn finding no room in time is refused as busy.

## Cluster

`connectors/cluster` uses NATS JetStream, and the `cortex` server enables it with `CORTEX_NATS_URL`.

- Turns go to a work-queue stream `cortex.<name>.turns`, and any instance can run them.
- A per-conversation lock (60 s TTL, renewed every 10 s) keeps a conversation's turns in order.
- Events are relayed to the instance holding the caller.
- History lives in a KV store, capped at 768 KiB per conversation with the oldest exchanges dropped.
- Skill reloads are announced to every instance.
- Shutdown drains for up to 90 s by default.
