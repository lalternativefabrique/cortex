package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Client is the model transport. The loop depends on this interface only, so
// a test can drive Run with a scripted client and no network.
type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// CompletionRequest is one model call.
//
// Field order in the wire payload is fixed by the marshalling in this file
// and messages are only ever appended, which is what lets an inference
// server reuse its KV cache across the steps of a run.
type CompletionRequest struct {
	System   string
	Messages []Message
	Tools    []Tool
}

// CompletionResponse is a single model turn.
type CompletionResponse struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	// Reasoning is what a thinking model worked through before answering.
	// It is not part of the answer and never goes back into the history —
	// it is there to be shown, and to explain a turn that ended with nothing
	// else to say.
	Reasoning string
	// StopReason is the provider's raw finish_reason, kept for diagnostics.
	StopReason string
}

// Provider is the config needed to reach an OpenAI-compatible endpoint:
// vLLM, SGLang and llama.cpp locally, Mistral, DeepSeek or any hosted
// gateway remotely.
type Provider struct {
	BaseURL string
	APIKey  string
	Model   string
	Headers map[string]string
	// Temperature is sent only when non-nil, so the server's own default
	// applies otherwise.
	Temperature *float64
	MaxTokens   int
	// ReasoningEffort controls how much a reasoning model thinks before
	// answering. Empty leaves the server's default alone; "none" turns
	// reasoning off, which matters because reasoning is billed as output
	// tokens — the dearest kind.
	ReasoningEffort string
	Timeout         time.Duration
	// MaxRetries bounds retries on 429 and 5xx. Zero means defaultMaxRetries.
	MaxRetries int
	HTTPClient *http.Client
}

const (
	defaultTimeout    = 10 * time.Minute
	defaultMaxRetries = 4
)

type httpClient struct {
	provider Provider
	http     *http.Client
	endpoint string
}

// NewClient returns a Client speaking the OpenAI chat-completions protocol.
//
// BaseURL must include the API version segment, e.g. http://localhost:8000/v1.
func NewClient(p Provider) (Client, error) {
	if p.BaseURL == "" {
		return nil, errors.New("provider: BaseURL is required")
	}
	if p.Model == "" {
		return nil, errors.New("provider: Model is required")
	}
	if p.Timeout == 0 {
		p.Timeout = defaultTimeout
	}
	if p.MaxRetries == 0 {
		p.MaxRetries = defaultMaxRetries
	}
	hc := p.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: p.Timeout}
	}
	return &httpClient{
		provider: p,
		http:     hc,
		endpoint: strings.TrimRight(p.BaseURL, "/") + "/chat/completions",
	}, nil
}

type wireRequest struct {
	Model       string        `json:"model"`
	Messages    []wireMessage `json:"messages"`
	Tools       []wireTool    `json:"tools,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// ReasoningEffort is ignored by servers that do not serve reasoning
	// models, so it is safe to send wherever the operator asked for it.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Stream          bool   `json:"stream"`
	// StreamOptions asks the server to include a final usage chunk, which
	// most OpenAI-compatible servers omit from a stream otherwise.
	StreamOptions *wireStreamOptions `json:"stream_options,omitempty"`
}

type wireStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolCallFunc `json:"function"`
}

type wireToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			// A reasoning model puts its thinking here and leaves content
			// empty until it is done. Cut short — by a step budget, or a
			// max_tokens — that is the whole of what it said, and dropping it
			// leaves an answer that looks blank for no stated reason.
			Reasoning        string         `json:"reasoning"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []wireToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// firstNonEmpty picks the field a provider actually filled: they disagree on
// whether reasoning arrives as "reasoning" or "reasoning_content".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (c *httpClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	payload, err := c.buildPayload(req)
	if err != nil {
		return CompletionResponse{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.provider.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt, lastErr)
			select {
			case <-ctx.Done():
				return CompletionResponse{}, ctx.Err()
			case <-time.After(delay):
			}
		}
		resp, err := c.do(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) {
			return CompletionResponse{}, err
		}
	}
	return CompletionResponse{}, fmt.Errorf("after %d retries: %w", c.provider.MaxRetries, lastErr)
}

func (c *httpClient) buildPayload(req CompletionRequest) (wireRequest, error) {
	msgs := make([]wireMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, wireMessage{Role: string(RoleSystem), Content: req.System})
	}
	for _, m := range req.Messages {
		wm := wireMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunc{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		msgs = append(msgs, wm)
	}

	tools := make([]wireTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		params, err := SchemaFor(t.InputSchema())
		if err != nil {
			return wireRequest{}, fmt.Errorf("schema for tool %q: %w", t.Name(), err)
		}
		tools = append(tools, wireTool{
			Type: "function",
			Function: wireFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  params,
			},
		})
	}

	return wireRequest{
		Model:           c.provider.Model,
		Messages:        msgs,
		Tools:           tools,
		Temperature:     c.provider.Temperature,
		MaxTokens:       c.provider.MaxTokens,
		ReasoningEffort: wireReasoningEffort(c.provider.ReasoningEffort),
		Stream:          false,
	}, nil
}

func (c *httpClient) do(ctx context.Context, body []byte) (CompletionResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.provider.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.provider.APIKey)
	}
	for k, v := range c.provider.Headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, &transportError{err: err}
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 32<<20))
	if err != nil {
		return CompletionResponse{}, &transportError{err: err}
	}

	if httpResp.StatusCode != http.StatusOK {
		return CompletionResponse{}, statusError(httpResp, raw, c.provider.APIKey != "")
	}

	var wr wireResponse
	if err := json.Unmarshal(raw, &wr); err != nil {
		return CompletionResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if wr.Error != nil {
		return CompletionResponse{}, fmt.Errorf("provider error: %s", wr.Error.Message)
	}
	if len(wr.Choices) == 0 {
		return CompletionResponse{}, errors.New("provider returned no choices")
	}

	choice := wr.Choices[0]
	out := CompletionResponse{
		Text:       choice.Message.Content,
		Reasoning:  firstNonEmpty(choice.Message.Reasoning, choice.Message.ReasoningContent),
		StopReason: choice.FinishReason,
		Usage: Usage{
			Input:       wr.Usage.PromptTokens,
			Output:      wr.Usage.CompletionTokens,
			CachedInput: wr.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	for _, tc := range choice.Message.ToolCalls {
		args := tc.Function.Arguments
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: json.RawMessage(args),
		})
	}
	return out, nil
}

// wireReasoningEffort maps an effort onto what goes on the wire.
//
// "none" is not a value every server takes: vLLM validates the field against
// low, medium and high and rejects the request outright. Asking for no
// reasoning is expressed by not asking for any, which every server
// understands as leaving its own default alone — and for a model that does
// not reason, that default is not to.
func wireReasoningEffort(effort string) string {
	if effort == ReasoningEffortNone {
		return ""
	}
	return effort
}

// transportError marks a network-level failure, always retryable.
type transportError struct{ err error }

func (e *transportError) Error() string { return "transport: " + e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// APIError is a non-2xx response from the provider.
type APIError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
	// hadKey distinguishes a rejected key from an absent one, which call for
	// different fixes.
	hadKey bool
}

func (e *APIError) Error() string {
	// A 401 is nearly always a missing or wrong key, and the provider's own
	// JSON says less about that than naming the setting does.
	if e.StatusCode == http.StatusUnauthorized {
		if e.hadKey {
			return "the API key was rejected: check SKODE_API_KEY, or -api-key"
		}
		return "no API key is set: export SKODE_API_KEY, or pass -api-key"
	}
	return fmt.Sprintf("provider returned %d: %s", e.StatusCode, truncateForError(e.Body))
}

func statusError(resp *http.Response, body []byte, hadKey bool) error {
	e := &APIError{StatusCode: resp.StatusCode, Body: string(body), hadKey: hadKey}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				e.RetryAfter = d
			}
		}
	}
	return e
}

func isRetryable(err error) bool {
	var te *transportError
	if errors.As(err, &te) {
		return true
	}
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode == http.StatusTooManyRequests || ae.StatusCode >= 500
	}
	return false
}

// backoff honors a server-provided Retry-After when present, and otherwise
// doubles the delay per attempt up to a minute.
func backoff(attempt int, err error) time.Duration {
	var ae *APIError
	if errors.As(err, &ae) && ae.RetryAfter > 0 {
		return ae.RetryAfter
	}
	d := time.Duration(1<<uint(attempt-1)) * time.Second
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

func truncateForError(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
