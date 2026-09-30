package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Task is one action an app declares for its agent: what it does, the
// instructions a turn of it runs under, and the JSON Schemas of what it
// takes and returns. A turn names it with SkillKey.
type Task struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	Instructions string            `json:"instructions"`
	Input        json.RawMessage   `json:"input,omitempty"`
	Output       json.RawMessage   `json:"output,omitempty"`
	Examples     []json.RawMessage `json:"examples,omitempty"`
}

// Catalog is what an app publishes, tasks.json.
type Catalog struct {
	Tasks []Task `json:"tasks"`
}

// declaredTask is a Task with its input schema compiled.
type declaredTask struct {
	Task
	input *jsonschema.Schema
}

func compileTasks(tasks []Task) (map[string]declaredTask, error) {
	out := make(map[string]declaredTask, len(tasks))
	for _, t := range tasks {
		if strings.TrimSpace(t.ID) == "" {
			return nil, fmt.Errorf("task: an id is required")
		}
		if _, dup := out[t.ID]; dup {
			return nil, fmt.Errorf("task %q: declared twice", t.ID)
		}
		d := declaredTask{Task: t}
		if len(t.Input) > 0 {
			schema, err := compileSchema(t.Input)
			if err != nil {
				return nil, fmt.Errorf("task %q input: %w", t.ID, err)
			}
			d.input = schema
		}
		if len(t.Output) > 0 {
			if _, err := compileSchema(t.Output); err != nil {
				return nil, fmt.Errorf("task %q output: %w", t.ID, err)
			}
		}
		out[t.ID] = d
	}
	return out, nil
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("schema.json")
}

// checkInput refuses an input that does not match the task's schema before
// any model call is paid for.
func (d declaredTask) checkInput(text string) error {
	if d.input == nil {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("task %q takes JSON input: %v", d.ID, err)
	}
	if err := d.input.Validate(doc); err != nil {
		return fmt.Errorf("task %q input does not match its schema: %v", d.ID, err)
	}
	return nil
}

func (d declaredTask) outputSchema() (map[string]any, bool) {
	if len(d.Output) == 0 {
		return nil, false
	}
	var schema map[string]any
	if json.Unmarshal(d.Output, &schema) != nil || len(schema) == 0 {
		return nil, false
	}
	return schema, true
}

func (d declaredTask) skill() a2a.AgentSkill {
	s := a2a.AgentSkill{ID: d.ID, Name: d.Name, Description: d.Description, Tags: []string{"task"}}
	if s.Name == "" {
		s.Name = d.ID
	}
	for _, ex := range d.Examples {
		s.Examples = append(s.Examples, string(ex))
	}
	if len(d.Input) > 0 {
		s.InputModes = []string{"application/json"}
	}
	if len(d.Output) > 0 {
		s.OutputModes = []string{"application/json"}
	}
	return s
}

func (h *Host) serveTasks(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Catalog{Tasks: h.cfg.Tasks})
}
