package agent

import (
	"encoding/json"
	"fmt"

	"github.com/invopop/jsonschema"
)

// SchemaFor derives a JSON Schema from a Go value annotated with
// `jsonschema:"..."` tags.
//
// The schema is inlined rather than referenced: providers reject $ref/$defs
// in function parameters, and the top-level $schema key is dropped for the
// same reason.
func SchemaFor(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`), nil
	}
	// A tool may supply its schema directly rather than as a Go type — an
	// MCP server publishes one this harness did not author, and reflecting
	// over a stand-in type would lose it.
	if pre, ok := v.(json.Marshaler); ok {
		raw, err := pre.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal supplied schema: %w", err)
		}
		return raw, nil
	}
	r := &jsonschema.Reflector{
		Anonymous:                  true,
		DoNotReference:             true,
		AllowAdditionalProperties:  true,
		RequiredFromJSONSchemaTags: false,
		ExpandedStruct:             true,
	}
	s := r.Reflect(v)
	s.Version = ""
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	return raw, nil
}
