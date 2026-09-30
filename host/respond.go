package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/lalternative/packages/go/cortex/agent"
)

// respondTool is how a turn that asked for an output schema ends: the model
// calls respond with its answer as arguments, which the model's API already
// structures as JSON, and the host checks them against the caller's schema.
// A valid answer ends the turn; an invalid one is sent back to the model to
// correct.
type respondTool struct {
	schema    json.RawMessage
	validator *jsonschema.Schema
	done      context.CancelFunc

	mu     sync.Mutex
	answer json.RawMessage
}

const respondName = "respond"

func newRespondTool(schema map[string]any, done context.CancelFunc) (*respondTool, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("output.json", doc); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	validator, err := c.Compile("output.json")
	if err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	return &respondTool{schema: raw, validator: validator, done: done}, nil
}

func (t *respondTool) Name() string { return respondName }

func (t *respondTool) Description() string {
	return "Give your final answer. Call it once, when you are done, with the answer as its arguments; never write the answer as text."
}

// InputSchema is the caller's schema as sent: a json.RawMessage passes
// through agent.SchemaFor untouched, where a map would be reflected as a
// bare object.
func (t *respondTool) InputSchema() any { return t.schema }

func (t *respondTool) Execute(_ context.Context, args json.RawMessage) (agent.ToolResult, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return agent.ToolResult{Content: "Your arguments are not JSON: " + err.Error() + ". Call respond again with valid arguments."}, nil
	}
	if err := t.validator.Validate(doc); err != nil {
		return agent.ToolResult{Content: "Your answer does not match the expected format: " + err.Error() + ". Call respond again with a corrected answer."}, nil
	}
	t.mu.Lock()
	t.answer = append(json.RawMessage(nil), args...)
	t.mu.Unlock()
	t.done()
	return agent.ToolResult{Content: "Answer recorded."}, nil
}

func (t *respondTool) recorded() (json.RawMessage, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.answer, t.answer != nil
}

const respondInstruction = "This turn's answer has a required format: end the turn by calling respond with the answer as its arguments. Do not write the answer as text."

const respondReminder = "You did not call respond. Call respond now with your answer as its arguments."

// maxRespondReminders bounds how many times a turn that ended in text is
// asked again to call respond before it fails.
const maxRespondReminders = 2

func outputSchema(meta map[string]any) (map[string]any, bool) {
	schema, ok := meta[OutputSchemaKey].(map[string]any)
	return schema, ok && len(schema) > 0
}

func withRespondInstruction(system string) string {
	return strings.TrimSpace(system + "\n\n" + respondInstruction)
}
