// Package task is how an app declares the actions its cortex agent runs,
// once, in Go: what each does, its instructions, and the types it takes and
// returns. The declarations become the app's tasks.json, which the agent
// loads, lists on its Agent Card and runs by name.
package task

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/client"
	"github.com/lalternative/packages/go/cortex/host"
)

// Spec describes a task. In is what the app sends, Out what it gets back;
// their JSON Schemas are reflected from their json and jsonschema tags. An
// Out of string is a text answer, with no output schema.
type Spec[In any] struct {
	ID           string
	Name         string
	Description  string
	Instructions string
	Examples     []In
}

// Task is a declared action: its declaration, and the call that runs it.
type Task[In, Out any] struct {
	declaration host.Task
}

// Declare builds a task from its spec. It panics on a spec that cannot be
// declared, as tasks are package-level values checked at start-up.
func Declare[In, Out any](s Spec[In]) Task[In, Out] {
	if s.ID == "" {
		panic("task: an id is required")
	}
	d := host.Task{ID: s.ID, Name: s.Name, Description: s.Description, Instructions: s.Instructions}
	var in In
	d.Input = mustSchema(s.ID, "input", &in)
	if !isText[Out]() {
		var out Out
		d.Output = mustSchema(s.ID, "output", &out)
	}
	for _, ex := range s.Examples {
		raw, err := json.Marshal(ex)
		if err != nil {
			panic(fmt.Sprintf("task %q example: %v", s.ID, err))
		}
		d.Examples = append(d.Examples, raw)
	}
	return Task[In, Out]{declaration: d}
}

func mustSchema(id, which string, v any) json.RawMessage {
	raw, err := agent.SchemaFor(v)
	if err != nil {
		panic(fmt.Sprintf("task %q %s schema: %v", id, which, err))
	}
	return raw
}

func isText[T any]() bool {
	return reflect.TypeFor[T]().Kind() == reflect.String
}

// Declaration is the task as tasks.json and the agent see it.
func (t Task[In, Out]) Declaration() host.Task { return t.declaration }

// Call is who and what a run acts for, besides its input.
type Call struct {
	Subject        string
	ConversationID string
	MCPHeaders     map[string]string
	// Context is added to the task's instructions for this run only.
	Context string
}

// Run sends in to the agent as this task and returns its answer as an Out,
// already checked against the task's output schema by the agent.
func (t Task[In, Out]) Run(ctx context.Context, a client.Agent, in In, c Call) (Out, client.Turn, error) {
	var out Out
	raw, err := json.Marshal(in)
	if err != nil {
		return out, client.Turn{}, fmt.Errorf("task %q input: %w", t.declaration.ID, err)
	}
	req := client.Request{
		Text:           string(raw),
		Context:        c.Context,
		Subject:        c.Subject,
		ConversationID: c.ConversationID,
		MCPHeaders:     c.MCPHeaders,
		Skill:          t.declaration.ID,
	}
	if isText[Out]() {
		text, turn, err := client.Say(ctx, a, req)
		if err != nil {
			return out, turn, err
		}
		reflect.ValueOf(&out).Elem().SetString(text)
		return out, turn, nil
	}
	return client.Ask[Out](ctx, a, req)
}

// Declared is any task, whatever its types.
type Declared interface {
	Declaration() host.Task
}

// Catalog gathers declarations into what an app publishes.
func Catalog(tasks ...Declared) host.Catalog {
	c := host.Catalog{Tasks: make([]host.Task, 0, len(tasks))}
	for _, t := range tasks {
		c.Tasks = append(c.Tasks, t.Declaration())
	}
	return c
}

// WriteFile writes the catalog as tasks.json, for go:generate.
func WriteFile(path string, tasks ...Declared) error {
	raw, err := json.MarshalIndent(Catalog(tasks...), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
