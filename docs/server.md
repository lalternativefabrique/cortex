# Running the `cortex` server

`connectors/cmd/cortex` is one server process with no flags, configured by environment. It listens on `CORTEX_ADDR` (default `:7400`).

```bash
go install github.com/lalternativefabrique/cortex/connectors/cmd/cortex@latest
```

| Area | Variables |
| --- | --- |
| Agent | `CORTEX_AGENT_NAME`, `CORTEX_AGENT_DESCRIPTION`, `CORTEX_INSTRUCTIONS`, `CORTEX_INSTRUCTIONS_FILE` |
| Model | `CORTEX_BASE_URL`, `CORTEX_API_KEY`, `CORTEX_MODEL` (default `lalter`), `CORTEX_REASONING_EFFORT` |
| Bounds | `CORTEX_MAX_STEPS`, `CORTEX_MAX_INPUT_TOKENS`, `CORTEX_CONTEXT_WINDOW` |
| Auth | `CORTEX_TOKEN`, `CORTEX_ISSUER_URL`, `CORTEX_AUDIENCE`, `CORTEX_JWKS_URL`, `CORTEX_CALLERS`, `CORTEX_PUBLIC_URL` |
| MCP | `CORTEX_MCP_CONFIG`, `CORTEX_MCP_SERVERS` (`name=url,...`) |
| Recall | `CORTEX_RECALL_URL`, `CORTEX_RECALL_KEY` |
| Skills | `CORTEX_SKILLS_FILE`, `CORTEX_SKILLS_URL` |
| Web | `CORTEX_TORNAD_URL`, `CORTEX_TORNAD_KEY` |
| Cluster | `CORTEX_NATS_URL`, `CORTEX_NATS_USER`, `CORTEX_NATS_PASSWORD`, `CORTEX_CONCURRENCY`, `CORTEX_QUEUE_WAIT`, `CORTEX_HISTORY_TTL` (default 7 days) |
| Telemetry | `SKALPAI_ENDPOINT`, `SKALPAI_API_KEY` |

`CORTEX_TOKEN` or `CORTEX_ISSUER_URL` is required.

## Connectors

| Package | Role |
| --- | --- |
| `cluster` | NATS turns, history, skill refresh |
| `recall/lalter` | Recall store over the L'Alter API |
| `vision` | Image and document description |
| `web/fetchpage` | Page reader |
| `web/websearch` | Search through Tornad or SearXNG |
| `web/geocoding`, `web/mappy` | Places |
| `web/sitemap` | Site exploration |
| `web/weather` | Forecast |
