package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Callback receives progress events. Every method may be called from the
// goroutine running the loop and from tool goroutines, so implementations
// must be safe for concurrent use.
//
// A nil Callback field is never called; embed NopCallback to implement only
// what you need.
type Callback interface {
	OnStepStart(step int)
	OnModelEnd(step int, text string, toolCalls []ToolCall, usage Usage)
	OnToolStart(name string, args json.RawMessage)
	OnToolEnd(trace ToolCallTrace)
	OnTextDelta(text string)
	OnEvict(freedTokens int)
	OnCompactStart(usedTokens, thresholdTokens int)
	OnCompactEnd(beforeTokens, afterTokens int)
	OnError(err error)
}

// NopCallback implements Callback with no-ops.
type NopCallback struct{}

func (NopCallback) OnStepStart(int)                           {}
func (NopCallback) OnModelEnd(int, string, []ToolCall, Usage) {}
func (NopCallback) OnToolStart(string, json.RawMessage)       {}
func (NopCallback) OnToolEnd(ToolCallTrace)                   {}
func (NopCallback) OnTextDelta(string)                        {}
func (NopCallback) OnEvict(int)                               {}
func (NopCallback) OnCompactStart(int, int)                   {}
func (NopCallback) OnCompactEnd(int, int)                     {}
func (NopCallback) OnError(error)                             {}

// Config parameterises a Runner.
type Config struct {
	Client Client
	Tools  []Tool
	// System is the stable prefix. It is sent first on every step and must
	// not embed volatile values — a timestamp or a git hash in here defeats
	// the inference server's prefix cache for the whole run.
	System string
	// MaxSteps bounds model calls per run. A coding task routinely needs
	// dozens; zero means DefaultMaxSteps.
	MaxSteps int
	// ToolTimeout caps a single tool execution. Zero means DefaultToolTimeout.
	ToolTimeout time.Duration
	// MaxToolResultBytes caps what a single tool result contributes to the
	// context. Zero means DefaultMaxToolResultBytes.
	MaxToolResultBytes int
	// ContextWindow is the model's context length in tokens. Zero disables
	// compaction, which is right for short runs and for servers whose window
	// is unknown.
	ContextWindow int
	// CompactAt is the fraction of ContextWindow at which compaction runs.
	// Zero means DefaultCompactAt.
	CompactAt float64
	// Compactor shrinks the history when the threshold is crossed. Nil with
	// a non-zero ContextWindow installs a SummaryCompactor over Client.
	Compactor Compactor
	// EvictKeepRecent is how many trailing messages are exempt from
	// eviction. Zero means DefaultEvictKeepRecent.
	EvictKeepRecent int
	// Stream emits model text through Callback.OnTextDelta as it arrives,
	// when the client supports it.
	Stream bool
	// Recorder persists each message as it is produced. Optional.
	Recorder Recorder
	Callback Callback
}

// Recorder persists conversation messages as a run proceeds, so an
// interrupted session can be resumed.
type Recorder interface {
	Append(Message) error
}

const (
	DefaultMaxSteps           = 60
	DefaultToolTimeout        = 2 * time.Minute
	DefaultMaxToolResultBytes = 16000
	DefaultCompactAt          = 0.75
	DefaultEvictKeepRecent    = 8
	// maxLeakRecoveries bounds retries of a model that keeps writing tool
	// calls as text. Past a couple of attempts it is not going to correct
	// itself, and the run should end with what it said rather than loop.
	maxLeakRecoveries = 2
)

// Runner executes the agentic loop.
type Runner struct {
	cfg   Config
	tools map[string]Tool
	// toolTokens is the cost of the tool definitions, computed once because
	// they never change within a run.
	toolTokens int
}

// NewRunner validates cfg and returns a Runner. Tool names must be unique.
func NewRunner(cfg Config) (*Runner, error) {
	if cfg.Client == nil {
		return nil, errors.New("config: Client is required")
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.ToolTimeout == 0 {
		cfg.ToolTimeout = DefaultToolTimeout
	}
	if cfg.MaxToolResultBytes == 0 {
		cfg.MaxToolResultBytes = DefaultMaxToolResultBytes
	}
	if cfg.CompactAt == 0 {
		cfg.CompactAt = DefaultCompactAt
	}
	if cfg.EvictKeepRecent == 0 {
		cfg.EvictKeepRecent = DefaultEvictKeepRecent
	}
	if cfg.Compactor == nil && cfg.ContextWindow > 0 {
		cfg.Compactor = &SummaryCompactor{Client: cfg.Client}
	}
	if cfg.Callback == nil {
		cfg.Callback = NopCallback{}
	}
	tools := make(map[string]Tool, len(cfg.Tools))
	for _, t := range cfg.Tools {
		if _, dup := tools[t.Name()]; dup {
			return nil, fmt.Errorf("duplicate tool name %q", t.Name())
		}
		tools[t.Name()] = t
	}
	return &Runner{cfg: cfg, tools: tools, toolTokens: EstimateTools(cfg.Tools)}, nil
}

// Run drives the loop until the model answers without requesting tools, the
// step budget is exhausted, or ctx is cancelled.
//
// Messages carries the conversation so far and is not mutated; the returned
// Result reports what the run produced.
func (r *Runner) Run(ctx context.Context, messages []Message) (Result, error) {
	history := make([]Message, len(messages))
	copy(history, messages)

	var result Result
	for step := 1; step <= r.cfg.MaxSteps; step++ {
		result.Steps = step
		r.cfg.Callback.OnStepStart(step)

		history = r.evict(history)

		if compacted, did, err := r.maybeCompact(ctx, history); err != nil {
			// A failed compaction is not fatal on its own: the next model
			// call may still fit. Report it and carry on with the history
			// as it stands.
			r.cfg.Callback.OnError(fmt.Errorf("compaction: %w", err))
		} else if did {
			history = compacted
			result.Compactions++
		}

		resp, err := r.complete(ctx, CompletionRequest{
			System:   r.cfg.System,
			Messages: history,
			Tools:    r.cfg.Tools,
		})
		if err != nil {
			r.cfg.Callback.OnError(err)
			return result, fmt.Errorf("step %d: %w", step, err)
		}
		result.Usage.Add(resp.Usage)
		r.cfg.Callback.OnModelEnd(step, resp.Text, resp.ToolCalls, resp.Usage)

		assistant := Message{
			Role:      RoleAssistant,
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		}
		history = append(history, assistant)
		r.record(assistant)

		if len(resp.ToolCalls) == 0 {
			// A model that wrote its tool call as text was not finished; it
			// fell back to the dialect it was trained on and the server did
			// not parse it. Ending here would abandon a run mid-thought.
			if LeakedToolCall(resp.Text) && result.Recoveries < maxLeakRecoveries {
				result.Recoveries++
				r.cfg.Callback.OnError(fmt.Errorf("model wrote a tool call as text (%s); asking it to retry", firstLeakedMarker(resp.Text)))
				notice := Message{Role: RoleUser, Content: leakedCallNotice}
				history = append(history, notice)
				r.record(notice)
				continue
			}
			result.Text = resp.Text
			return result, nil
		}

		results := r.executeAll(ctx, resp.ToolCalls)
		for i, tc := range resp.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, results[i].trace)
			toolMsg := Message{
				Role:       RoleTool,
				ToolCallID: tc.ID,
				Content:    results[i].content,
			}
			history = append(history, toolMsg)
			r.record(toolMsg)
		}
	}

	result.Truncated = true
	return result, nil
}

type toolOutcome struct {
	content string
	trace   ToolCallTrace
}

// executeAll runs the calls of one model turn concurrently and returns the
// outcomes in request order, which is the order the provider requires the
// tool messages to appear in.
func (r *Runner) executeAll(ctx context.Context, calls []ToolCall) []toolOutcome {
	out := make([]toolOutcome, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func(i int, call ToolCall) {
			defer wg.Done()
			out[i] = r.executeOne(ctx, call)
		}(i, call)
	}
	wg.Wait()
	return out
}

func (r *Runner) executeOne(ctx context.Context, call ToolCall) toolOutcome {
	r.cfg.Callback.OnToolStart(call.Name, call.Arguments)
	started := time.Now()

	trace := ToolCallTrace{
		Name:      call.Name,
		Arguments: string(call.Arguments),
	}
	finish := func(content string, err error) toolOutcome {
		trace.DurationMs = time.Since(started).Milliseconds()
		trace.Result = content
		if err != nil {
			trace.Err = err.Error()
		}
		r.cfg.Callback.OnToolEnd(trace)
		return toolOutcome{content: content, trace: trace}
	}

	tool, ok := r.tools[call.Name]
	if !ok {
		return finish(fmt.Sprintf("error: unknown tool %q", call.Name), nil)
	}

	tctx, cancel := context.WithTimeout(ctx, r.cfg.ToolTimeout)
	defer cancel()

	res, err := tool.Execute(tctx, call.Arguments)
	if err != nil {
		// A tool error is reported to the model as a result rather than
		// raised: the model can then correct its arguments instead of the
		// run dying on a recoverable mistake.
		return finish(fmt.Sprintf("error: %v", err), err)
	}

	content, truncated := TruncateMiddle(res.Content, r.cfg.MaxToolResultBytes)
	if truncated {
		trace.Result = content
	}
	return finish(content, nil)
}

// evict blanks tool results a later call has superseded. It runs before
// compaction because it is nearly free and preserves the exact wording of
// what remains, where a summary does not — and because dropping stale bytes
// may keep the history under the compaction threshold entirely.
func (r *Runner) evict(history []Message) []Message {
	if r.cfg.ContextWindow <= 0 {
		return history
	}
	out, freed := EvictStale(history, r.cfg.EvictKeepRecent)
	if freed > 0 {
		r.cfg.Callback.OnEvict(freed)
	}
	return out
}

// maybeCompact shrinks the history when it approaches the context window,
// reporting whether it did.
func (r *Runner) maybeCompact(ctx context.Context, history []Message) ([]Message, bool, error) {
	if r.cfg.ContextWindow <= 0 || r.cfg.Compactor == nil {
		return history, false, nil
	}
	used := EstimateMessages(r.cfg.System, history) + r.toolTokens
	threshold := int(float64(r.cfg.ContextWindow) * r.cfg.CompactAt)
	if used < threshold {
		return history, false, nil
	}

	r.cfg.Callback.OnCompactStart(used, threshold)
	compacted, err := r.cfg.Compactor.Compact(ctx, r.cfg.System, history)
	if err != nil {
		return history, false, err
	}
	r.cfg.Callback.OnCompactEnd(used, EstimateMessages(r.cfg.System, compacted)+r.toolTokens)
	return compacted, true, nil
}

// complete streams the model call when both the client and the caller
// support it, and otherwise falls back to a single blocking call.
func (r *Runner) complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if !r.cfg.Stream {
		return r.cfg.Client.Complete(ctx, req)
	}
	sc, ok := r.cfg.Client.(StreamingClient)
	if !ok {
		return r.cfg.Client.Complete(ctx, req)
	}
	return sc.CompleteStream(ctx, req, r.cfg.Callback.OnTextDelta)
}

// record persists a message, reporting a failure without ending the run:
// losing the ability to resume is worse than losing the work in progress.
func (r *Runner) record(m Message) {
	if r.cfg.Recorder == nil {
		return
	}
	if err := r.cfg.Recorder.Append(m); err != nil {
		r.cfg.Callback.OnError(fmt.Errorf("record message: %w", err))
	}
}
