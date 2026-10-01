package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Skill is one action an app declares for its agent (an A2A AgentSkill,
// with what A2A leaves out): what it does, the instructions a run of it
// follows, and the JSON Schemas of what it takes and returns. A message
// names it with SkillKey; each run is an A2A task.
type Skill struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	// Model is the model this skill runs on, chosen by the app for the
	// task; empty uses the agent's. A run may still ask for another.
	Model    string            `json:"model,omitempty"`
	Examples []json.RawMessage `json:"examples,omitempty"`
}

// Catalog is what an app publishes, skills.json: its own instructions,
// applied to every run of its agent, and its skills.
type Catalog struct {
	// Instructions are the app's: its doctrine, applied to every run after
	// the agent's own instructions and before a skill's.
	Instructions string  `json:"instructions,omitempty"`
	Skills       []Skill `json:"skills"`
}

// declaredSkill is a Skill with its input schema compiled.
type declaredSkill struct {
	Skill
	input *jsonschema.Schema
}

func compileSkills(skills []Skill) (map[string]declaredSkill, error) {
	out := make(map[string]declaredSkill, len(skills))
	for _, t := range skills {
		if strings.TrimSpace(t.ID) == "" {
			return nil, fmt.Errorf("skill: an id is required")
		}
		if _, dup := out[t.ID]; dup {
			return nil, fmt.Errorf("skill %q: declared twice", t.ID)
		}
		d := declaredSkill{Skill: t}
		if len(t.Input) > 0 {
			schema, err := compileSchema(t.Input)
			if err != nil {
				return nil, fmt.Errorf("skill %q input: %w", t.ID, err)
			}
			d.input = schema
		}
		if len(t.Output) > 0 {
			if _, err := compileSchema(t.Output); err != nil {
				return nil, fmt.Errorf("skill %q output: %w", t.ID, err)
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

// checkInput refuses an input that does not match the skill's schema before
// any model call is paid for.
func (d declaredSkill) checkInput(text string) error {
	if d.input == nil {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("skill %q takes JSON input: %v", d.ID, err)
	}
	if err := d.input.Validate(doc); err != nil {
		return fmt.Errorf("skill %q input does not match its schema: %v", d.ID, err)
	}
	return nil
}

func (d declaredSkill) outputSchema() (map[string]any, bool) {
	if len(d.Output) == 0 {
		return nil, false
	}
	var schema map[string]any
	if json.Unmarshal(d.Output, &schema) != nil || len(schema) == 0 {
		return nil, false
	}
	return schema, true
}

func (d declaredSkill) skill() a2a.AgentSkill {
	s := a2a.AgentSkill{ID: d.ID, Name: d.Name, Description: d.Description, Tags: []string{"skill"}}
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

func (h *Host) serveSkills(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.catalog())
}

func (h *Host) catalog() Catalog {
	h.skillsMu.RLock()
	defer h.skillsMu.RUnlock()
	return Catalog{Instructions: h.appInstructions, Skills: h.skillList}
}

// SetCatalog replaces the app's instructions and skills while the agent
// serves.
func (h *Host) SetCatalog(c Catalog) error {
	compiled, err := compileSkills(c.Skills)
	if err != nil {
		return err
	}
	h.skillsMu.Lock()
	defer h.skillsMu.Unlock()
	h.appInstructions = c.Instructions
	h.skillList = append([]Skill(nil), c.Skills...)
	h.skills = compiled
	return nil
}

func (h *Host) appInstructionsNow() string {
	h.skillsMu.RLock()
	defer h.skillsMu.RUnlock()
	return h.appInstructions
}

// SetSkills replaces the agent's declared skills while it serves, for an
// agent that loads them after it started, from an app that was not up yet.
func (h *Host) SetSkills(skills []Skill) error {
	compiled, err := compileSkills(skills)
	if err != nil {
		return err
	}
	h.skillsMu.Lock()
	defer h.skillsMu.Unlock()
	h.skillList = append([]Skill(nil), skills...)
	h.skills = compiled
	return nil
}

func (h *Host) declaredSkills() ([]Skill, map[string]declaredSkill) {
	h.skillsMu.RLock()
	defer h.skillsMu.RUnlock()
	return h.skillList, h.skills
}

// SetSkillSource tells the agent where its app's catalog comes from, so it
// can be loaded again on demand (POST /skills/refresh) when the app declares
// new skills or changes its instructions, without restarting the agent.
func (h *Host) SetSkillSource(source func(context.Context) (Catalog, error)) {
	h.skillsMu.Lock()
	defer h.skillsMu.Unlock()
	h.skillSource = source
}

// RefreshSkills loads the app's catalog again from its source and sets it.
func (h *Host) RefreshSkills(ctx context.Context) (int, error) {
	h.skillsMu.RLock()
	source := h.skillSource
	h.skillsMu.RUnlock()
	if source == nil {
		return 0, errNoSkillSource
	}
	c, err := source(ctx)
	if err != nil {
		return 0, err
	}
	if err := h.SetCatalog(c); err != nil {
		return 0, err
	}
	return len(c.Skills), nil
}

var errNoSkillSource = errors.New("this agent loads no skills from its app")

func (h *Host) refreshSkills(w http.ResponseWriter, r *http.Request) {
	n, err := h.RefreshSkills(r.Context())
	switch {
	case errors.Is(err, errNoSkillSource):
		http.Error(w, err.Error(), http.StatusNotFound)
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
	default:
		if h.cfg.OnSkillsRefreshed != nil {
			h.cfg.OnSkillsRefreshed(r.Context())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"skills": n})
	}
}
