// Package client calls a cortex agent over A2A (lalter ADR 0013): directly,
// or through lalter's door for a hosted agent.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/host"
)

// Agent is where a turn is sent. URL is the agent's A2A base, /a2a is
// appended; Header is added to every request, such as lalter's
// X-End-User-Id.
type Agent struct {
	URL    string
	Token  string
	Header http.Header
	HTTP   *http.Client
}

// Request is one turn.
type Request struct {
	Text string
	// Context is added to the agent's instructions for this turn only.
	Context string
	// Subject is the person the turn acts for.
	Subject string
	// ConversationID groups turns into one conversation; empty starts a new
	// one.
	ConversationID string
	// MCPHeaders ride on every MCP call of the turn, never to the model.
	MCPHeaders map[string]string
	// Skill names the declared skill the message runs; Text is then its
	// input.
	Skill string
	// Model asks for a model for this run in place of the agent's own.
	Model string
}

// ToolCall is one tool the agent called during the turn.
type ToolCall struct {
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Result    string          `json:"result"`
	Error     string          `json:"error"`
}

// Step is one model call of the turn: what the model reasoned, the tools it
// asked for, its tokens and duration.
type Step struct {
	Step              int      `json:"step"`
	Reasoning         string   `json:"reasoning"`
	ToolCalls         []string `json:"tool_calls"`
	InputTokens       int      `json:"input_tokens"`
	CachedInputTokens int      `json:"cached_input_tokens"`
	OutputTokens      int      `json:"output_tokens"`
	DurationMs        int64    `json:"duration_ms"`
}

// Turn is what a turn produced besides its answer.
type Turn struct {
	ToolCalls []ToolCall
	Steps     []Step
}

// Ask runs a turn whose answer must be a T: the schema of T is sent with
// the turn, the agent answers in that format, and the answer comes back
// decoded. The schema is reflected from T's json tags; a field's jsonschema
// description tag tells the model what to put in it.
func Ask[T any](ctx context.Context, a Agent, r Request) (T, Turn, error) {
	var out T
	schema, err := agent.SchemaFor(&out)
	if err != nil {
		return out, Turn{}, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return out, Turn{}, err
	}
	task, err := send(ctx, a, r, decoded)
	if err != nil {
		return out, Turn{}, err
	}
	data, ok := task.data()
	if !ok {
		return out, task.turn(), errors.New("cortex: the answer carries no data")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, task.turn(), fmt.Errorf("cortex: decode answer: %w", err)
	}
	return out, task.turn(), nil
}

// Say runs a turn answered in text.
func Say(ctx context.Context, a Agent, r Request) (string, Turn, error) {
	task, err := send(ctx, a, r, nil)
	if err != nil {
		return "", Turn{}, err
	}
	return task.text(), task.turn(), nil
}

type part struct {
	Kind string          `json:"kind"`
	Text string          `json:"text"`
	Data json.RawMessage `json:"data"`
}

type artifact struct {
	Name  string `json:"name"`
	Parts []part `json:"parts"`
}

type task struct {
	Status struct {
		State   string `json:"state"`
		Message *struct {
			Parts []part `json:"parts"`
		} `json:"message"`
	} `json:"status"`
	Artifacts []artifact `json:"artifacts"`
}

func (t task) data() (json.RawMessage, bool) {
	for _, a := range t.Artifacts {
		if a.Name != "answer" {
			continue
		}
		for _, p := range a.Parts {
			if p.Kind == "data" && len(p.Data) > 0 {
				return p.Data, true
			}
		}
	}
	return nil, false
}

func (t task) text() string {
	var b strings.Builder
	for _, a := range t.Artifacts {
		if a.Name == "answer" {
			for _, p := range a.Parts {
				b.WriteString(p.Text)
			}
		}
	}
	return b.String()
}

func (t task) turn() Turn {
	var turn Turn
	for _, a := range t.Artifacts {
		for _, p := range a.Parts {
			if p.Kind != "data" {
				continue
			}
			switch a.Name {
			case "tool-call":
				var call ToolCall
				if json.Unmarshal(p.Data, &call) == nil {
					turn.ToolCalls = append(turn.ToolCalls, call)
				}
			case "step":
				var step Step
				if json.Unmarshal(p.Data, &step) == nil {
					turn.Steps = append(turn.Steps, step)
				}
			}
		}
	}
	return turn
}

func send(ctx context.Context, a Agent, r Request, schema map[string]any) (task, error) {
	meta := map[string]any{}
	if r.Subject != "" {
		meta[host.SubjectKey] = r.Subject
	}
	if r.Context != "" {
		meta[host.TurnContextKey] = r.Context
	}
	if len(r.MCPHeaders) > 0 {
		meta[host.MCPHeadersKey] = r.MCPHeaders
	}
	if schema != nil {
		meta[host.OutputSchemaKey] = schema
	}
	if r.Skill != "" {
		meta[host.SkillKey] = r.Skill
	}
	if r.Model != "" {
		meta[host.ModelKey] = r.Model
	}
	conversation := r.ConversationID
	if conversation == "" {
		conversation = uuid.NewString()
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "message/send",
		"params": map[string]any{"message": map[string]any{
			"role": "user", "messageId": uuid.NewString(), "contextId": conversation,
			"parts":    []map[string]any{{"kind": "text", "text": r.Text}},
			"metadata": meta,
		}},
	})
	if err != nil {
		return task{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.URL, "/")+"/a2a", bytes.NewReader(body))
	if err != nil {
		return task{}, err
	}
	for k, vs := range a.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.Token)
	httpClient := a.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Minute}
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return task{}, fmt.Errorf("cortex: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return task{}, fmt.Errorf("cortex: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return task{}, fmt.Errorf("cortex answered %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var frame struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result task `json:"result"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return task{}, fmt.Errorf("cortex: %w", err)
	}
	if frame.Error != nil {
		return task{}, fmt.Errorf("cortex: %s", frame.Error.Message)
	}
	if frame.Result.Status.State != "completed" {
		why := frame.Result.Status.State
		if m := frame.Result.Status.Message; m != nil && len(m.Parts) > 0 {
			why = m.Parts[0].Text
		}
		return frame.Result, fmt.Errorf("cortex: %s", why)
	}
	return frame.Result, nil
}
