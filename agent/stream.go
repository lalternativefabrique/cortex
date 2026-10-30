package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// StreamingClient completes a request while emitting text as it arrives.
//
// Clients that cannot stream do not implement this; the loop falls back to
// Complete and the caller simply sees the text all at once.
type StreamingClient interface {
	Client
	CompleteStream(ctx context.Context, req CompletionRequest, onDelta func(string)) (CompletionResponse, error)
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Providers disagree on the name; a reasoning model sends one of
			// these and no content until it has finished thinking.
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// CompleteStream runs the request with stream=true and calls onDelta for each
// text fragment.
//
// Tool calls arrive fragmented across chunks — the name in one, the JSON
// arguments a few characters at a time in the next — so they are reassembled
// by index and only surfaced once the stream ends.
func (c *httpClient) CompleteStream(ctx context.Context, req CompletionRequest, onDelta func(string)) (CompletionResponse, error) {
	payload, err := c.buildPayload(req)
	if err != nil {
		return CompletionResponse{}, err
	}
	payload.Stream = true
	payload.StreamOptions = &wireStreamOptions{IncludeUsage: true}

	body, err := json.Marshal(payload)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if c.provider.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.provider.APIKey)
	}
	for k, v := range c.provider.Headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, &transportError{err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw := make([]byte, 4096)
		n, _ := resp.Body.Read(raw)
		return CompletionResponse{}, statusError(resp, raw[:n], c.provider.APIKey != "")
	}

	return readSSE(resp.Body, onDelta)
}

type accumulatingCall struct {
	id   string
	name string
	args strings.Builder
}

func readSSE(r interface{ Read([]byte) (int, error) }, onDelta func(string)) (CompletionResponse, error) {
	var out CompletionResponse
	var text strings.Builder
	var reasoning strings.Builder
	calls := map[int]*accumulatingCall{}
	var order []int

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// A malformed chunk is not worth aborting a response that is
			// otherwise arriving correctly.
			continue
		}
		if chunk.Usage != nil {
			out.Usage = Usage{
				Input:       chunk.Usage.PromptTokens,
				Output:      chunk.Usage.CompletionTokens,
				CachedInput: chunk.Usage.PromptTokensDetails.CachedTokens,
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			out.StopReason = choice.FinishReason
		}
		if d := choice.Delta.Content; d != "" {
			text.WriteString(d)
			if onDelta != nil {
				onDelta(d)
			}
		}
		// Kept, not streamed: a reasoning model sends this instead of content
		// while it thinks, and a turn that ends here would otherwise arrive
		// empty with nothing to show for the time it took.
		if d := firstNonEmpty(choice.Delta.Reasoning, choice.Delta.ReasoningContent); d != "" {
			reasoning.WriteString(d)
		}
		for _, tc := range choice.Delta.ToolCalls {
			acc, ok := calls[tc.Index]
			if !ok {
				acc = &accumulatingCall{}
				calls[tc.Index] = acc
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.args.WriteString(tc.Function.Arguments)
		}
	}
	if err := scanner.Err(); err != nil {
		return out, &transportError{err: err}
	}

	out.Text = text.String()
	out.Reasoning = reasoning.String()
	for _, idx := range order {
		acc := calls[idx]
		if acc.name == "" {
			continue
		}
		args := strings.TrimSpace(acc.args.String())
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return out, fmt.Errorf("tool call %q streamed invalid JSON arguments", acc.name)
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        acc.id,
			Name:      acc.name,
			Arguments: json.RawMessage(args),
		})
	}
	if out.Text == "" && len(out.ToolCalls) == 0 && out.StopReason == "" {
		return out, errors.New("stream ended without any content")
	}
	return out, nil
}
