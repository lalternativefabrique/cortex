# The agent loop

`agent.Runner.Run` drives the conversation: call the model, execute its tool calls, repeat until it answers or a bound stops it.

## Bounds

| `agent.Config` | Default | Effect |
| --- | --- | --- |
| `MaxSteps` | 60 | Ends the run as `Truncated` |
| `MaxInputTokens` | 0 (unbounded) | Ends the run as `Exhausted` |
| `StallAfter` | 2 (negative disables) | Ends a repeating run as `Stalled` |
| `ToolTimeout` | 2 min | Per tool call |
| `MaxToolResultBytes` | 16000 | Longer results are cut in the middle |

The first repeated call is not executed: the model gets a notice and a chance to change course. `CloseOnBound` grants one last tool-less model call so a bounded run can still conclude.

## How a run ends

- The model answers with no tool calls. Before accepting, the loop recovers tool calls leaked into text (up to twice), consumes pending steering messages, and can ask once for sources when a `SourceTools` tool was used and figures are unsourced.
- A bound above is reached.
- A model call fails after retries, which returns an error.

A failing tool does not end the run: the model receives `error: ...` and can react.

## Context

Compaction is active only when `ContextWindow` is above 0. Each step first blanks superseded tool results (`EvictStale`, keeping the 8 most recent), then compacts when needed.

The threshold starts at `CompactAt` (0.75). `DefaultCompactBudget` moves it to 0.6 when under 25% of the steps remain, and lowers it by 0.05 at 3 or more tool errors and at 2 or more repeats. `SummaryCompactor` keeps the first message and the 6 latest. A failed compaction does not stop the run.

## Retries

`Provider.MaxRetries` (default 4) covers 429, 5xx and transport errors. Backoff honours `Retry-After`, otherwise doubles per attempt up to one minute.

## Tools

A tool implements `agent.Tool`: `Name`, `Description`, `InputSchema`, `Execute`. `ToolResult.Metadata` never reaches the model.

Calls from one step run concurrently, one goroutine each with no cap, and results keep request order. `Toolset` and `load_tools` defer tool schemas until the model asks for them.

`Steer` injects user messages mid-run. `tools.NewDeepResearch` runs a nested loop, bounded by default to 40 steps and 400,000 input tokens.
