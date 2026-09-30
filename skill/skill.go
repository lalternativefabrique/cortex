// Package skill is how an app declares the actions its cortex agent runs,
// once, in Go: what each does, its instructions, and the types it takes and
// returns. The declarations become the app's skills.json, which the agent
// loads, lists on its Agent Card as A2A skills and runs by name; each run
// is an A2A task.
package skill

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

// Spec describes a skill. In is what the app sends, Out what it gets back;
// their JSON Schemas are reflected from their json and jsonschema tags. An
// Out of string is a text answer, with no output schema.
type Spec[In any] struct {
	ID           string
	Name         string
	Description  string
	Instructions string
	Examples     []In
}

// Skill is a declared action: its declaration, and the call that runs it.
type Skill[In, Out any] struct {
	declaration host.Skill
}

// Declare builds a skill from its spec. It panics on a spec that cannot be
// declared, as skills are package-level values checked at start-up.
func Declare[In, Out any](s Spec[In]) Skill[In, Out] {
	if s.ID == "" {
		panic("skill: an id is required")
	}
	d := host.Skill{ID: s.ID, Name: s.Name, Description: s.Description, Instructions: s.Instructions}
	var in In
	d.Input = mustSchema(s.ID, "input", &in)
	if !isText[Out]() {
		var out Out
		d.Output = mustSchema(s.ID, "output", &out)
	}
	for _, ex := range s.Examples {
		raw, err := json.Marshal(ex)
		if err != nil {
			panic(fmt.Sprintf("skill %q example: %v", s.ID, err))
		}
		d.Examples = append(d.Examples, raw)
	}
	return Skill[In, Out]{declaration: d}
}

func mustSchema(id, which string, v any) json.RawMessage {
	raw, err := agent.SchemaFor(v)
	if err != nil {
		panic(fmt.Sprintf("skill %q %s schema: %v", id, which, err))
	}
	return raw
}

func isText[T any]() bool {
	return reflect.TypeFor[T]().Kind() == reflect.String
}

// Declaration is the skill as skills.json and the agent see it.
func (t Skill[In, Out]) Declaration() host.Skill { return t.declaration }

// Call is who and what a run acts for, besides its input.
type Call struct {
	Subject        string
	ConversationID string
	MCPHeaders     map[string]string
	// Context is added to the skill's instructions for this run only.
	Context string
}

// Run sends in to the agent as this skill and returns its answer as an Out,
// already checked against the skill's output schema by the agent. The run
// is one A2A task.
func (t Skill[In, Out]) Run(ctx context.Context, a client.Agent, in In, c Call) (Out, client.Turn, error) {
	var out Out
	raw, err := json.Marshal(in)
	if err != nil {
		return out, client.Turn{}, fmt.Errorf("skill %q input: %w", t.declaration.ID, err)
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

// Declared is any skill, whatever its types.
type Declared interface {
	Declaration() host.Skill
}

// Catalog gathers declarations into what an app publishes.
func Catalog(skills ...Declared) host.Catalog {
	c := host.Catalog{Skills: make([]host.Skill, 0, len(skills))}
	for _, s := range skills {
		c.Skills = append(c.Skills, s.Declaration())
	}
	return c
}

// WriteFile writes the catalog as skills.json, for go:generate.
func WriteFile(path string, skills ...Declared) error {
	raw, err := json.MarshalIndent(Catalog(skills...), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
