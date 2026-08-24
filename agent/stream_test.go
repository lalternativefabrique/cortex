package agent

import (
	"strings"
	"testing"
)

func TestReadSSEAssemblesText(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"choices":[{"delta":{"content":", world"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	var deltas []string
	resp, err := readSSE(strings.NewReader(stream), func(s string) { deltas = append(deltas, s) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "Hello, world" {
		t.Fatalf("Text = %q", resp.Text)
	}
	if len(deltas) != 2 {
		t.Fatalf("got %d deltas, want 2 — text must reach the caller as it arrives", len(deltas))
	}
}

func TestReadSSEReassemblesFragmentedToolCall(t *testing.T) {
	// Providers split a tool call across chunks: the name arrives first, the
	// JSON arguments a few characters at a time.
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"go test\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := readSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "bash" {
		t.Fatalf("tool call = %+v", tc)
	}
	if string(tc.Arguments) != `{"command":"go test"}` {
		t.Fatalf("Arguments = %s", tc.Arguments)
	}
}

func TestReadSSEKeepsParallelToolCallsSeparate(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"read","arguments":"{\"path\":\"b.go\"}"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := readSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2 — indexes must not be merged", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "a" || resp.ToolCalls[1].ID != "b" {
		t.Fatalf("calls out of order: %+v", resp.ToolCalls)
	}
}

func TestReadSSECapturesUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":400}}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := readSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Input: 500, Output: 20, CachedInput: 400}
	if resp.Usage != want {
		t.Fatalf("Usage = %+v, want %+v — cached_tokens is how prefix stability is measured", resp.Usage, want)
	}
}

func TestReadSSESkipsMalformedChunk(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"before"}}]}`,
		`data: {not json at all`,
		`data: {"choices":[{"delta":{"content":" after"}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := readSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "before after" {
		t.Fatalf("Text = %q — one bad chunk must not discard a good response", resp.Text)
	}
}

func TestReadSSERejectsInvalidToolArguments(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"bash","arguments":"{\"command\":"}}]}}]}`,
		`data: [DONE]`,
		"",
	}, "\n")

	if _, err := readSSE(strings.NewReader(stream), nil); err == nil {
		t.Fatal("truncated tool arguments were accepted, which would reach the tool as garbage")
	}
}

func TestReadSSEReportsEmptyStream(t *testing.T) {
	if _, err := readSSE(strings.NewReader("data: [DONE]\n"), nil); err == nil {
		t.Fatal("an empty stream was treated as a valid response")
	}
}

func TestReadSSEIgnoresCommentsAndBlankLines(t *testing.T) {
	stream := strings.Join([]string{
		": keepalive",
		"",
		`data: {"choices":[{"delta":{"content":"ok"}}]}`,
		"",
		`data: [DONE]`,
		"",
	}, "\n")

	resp, err := readSSE(strings.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "ok" {
		t.Fatalf("Text = %q", resp.Text)
	}
}
