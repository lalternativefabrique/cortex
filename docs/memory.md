# Memory and skills

| Package | Holds | Backend |
| --- | --- | --- |
| `recall` | Memory across conversations, scoped by subject and agent; `recall_memory` tool | `recall.Store` interface; `connectors/recall/lalter` is one store. Nil means no memory |
| `memory` | Notes extracted by compaction, per project | Files under `$XDG_STATE_HOME/skode/memory` or `~/.local/state/skode/memory` |
| `session` | Resumable conversations | One JSONL per session under `skode/sessions` |
| `host.History` | Conversations between turns | In memory by default, or NATS KV |

## Skills

`skills` loads Markdown instructions with frontmatter from `~/.config/skode/skills`, `~/.claude/skills`, then `.ai/skills`. The repository wins.

`skill` is for apps: `Declare[In, Out]` declares a typed action, and `WriteCatalog` or `WriteFile` produce the `skills.json` the server reads from `CORTEX_SKILLS_FILE` or `CORTEX_SKILLS_URL`.
