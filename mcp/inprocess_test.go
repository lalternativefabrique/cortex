package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type stubHandler struct {
	tools []ToolDescriptor
	// called records what Call received, so a test can assert the arguments
	// crossed the transport unchanged.
	calledName string
	calledArgs string
	result     ToolResult
	err        error
}

func (h *stubHandler) Tools() []ToolDescriptor { return h.tools }

func (h *stubHandler) Call(_ context.Context, name string, args json.RawMessage) (ToolResult, error) {
	h.calledName = name
	h.calledArgs = string(args)
	return h.result, h.err
}

func TestInProcessOffersItsTools(t *testing.T) {
	h := &stubHandler{tools: []ToolDescriptor{
		{Name: "weather", Description: "Forecast for a place.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "geocode", Description: "Resolve a place name."},
	}}

	tools, err := ConnectHandler(context.Background(), "lalter", h)
	if err != nil {
		t.Fatalf("ConnectHandler: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	// The name is prefixed by the server, exactly as it is for a remote one.
	if !strings.HasPrefix(tools[0].Name(), "lalter") {
		t.Errorf("tool name %q is not prefixed by the server", tools[0].Name())
	}
	// The description keeps the handler's own text and gains the note the
	// client adds for every server, in-process or not.
	if !strings.Contains(tools[0].Description(), "Forecast for a place.") {
		t.Errorf("description is %q, want the handler's text", tools[0].Description())
	}
	if !strings.Contains(tools[0].Description(), "lalter") {
		t.Errorf("description is %q, want it to name the server", tools[0].Description())
	}
}

func TestInProcessCallsThrough(t *testing.T) {
	h := &stubHandler{
		tools:  []ToolDescriptor{{Name: "weather"}},
		result: ToolResult{Text: "rain tomorrow"},
	}

	tools, err := ConnectHandler(context.Background(), "lalter", h)
	if err != nil {
		t.Fatalf("ConnectHandler: %v", err)
	}

	res, err := tools[0].Execute(context.Background(), json.RawMessage(`{"place":"Pau"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if h.calledName != "weather" {
		t.Errorf("handler got name %q, want weather", h.calledName)
	}
	if !strings.Contains(h.calledArgs, "Pau") {
		t.Errorf("handler got arguments %q, want them to carry Pau", h.calledArgs)
	}
	if !strings.Contains(res.Content, "rain tomorrow") {
		t.Errorf("result is %q", res.Content)
	}
}

// A tool-level failure comes back as a result the model can react to, the
// same way a remote server's does — not as an error that ends the run.
func TestInProcessToolErrorIsAResult(t *testing.T) {
	h := &stubHandler{
		tools:  []ToolDescriptor{{Name: "weather"}},
		result: ToolResult{Text: "no such place", IsError: true},
	}

	tools, err := ConnectHandler(context.Background(), "lalter", h)
	if err != nil {
		t.Fatalf("ConnectHandler: %v", err)
	}

	res, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if !strings.Contains(res.Content, "no such place") {
		t.Errorf("result is %q, want the server's message", res.Content)
	}
}

func TestInProcessCallFailureIsAResult(t *testing.T) {
	h := &stubHandler{
		tools: []ToolDescriptor{{Name: "weather"}},
		err:   errors.New("backend unreachable"),
	}

	tools, err := ConnectHandler(context.Background(), "lalter", h)
	if err != nil {
		t.Fatalf("ConnectHandler: %v", err)
	}

	res, err := tools[0].Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute returned an error: %v", err)
	}
	if !strings.Contains(res.Content, "backend unreachable") {
		t.Errorf("result is %q, want the cause", res.Content)
	}
}

// A tool with no schema still has to advertise one: a model given a nil
// schema has nothing to build a call from.
func TestInProcessDefaultsTheSchema(t *testing.T) {
	h := &stubHandler{tools: []ToolDescriptor{{Name: "now"}}}

	tools, err := ConnectHandler(context.Background(), "lalter", h)
	if err != nil {
		t.Fatalf("ConnectHandler: %v", err)
	}
	schema, err := json.Marshal(tools[0].InputSchema())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	if !strings.Contains(string(schema), "object") {
		t.Errorf("schema is %s, want an object schema", schema)
	}
}

func TestConnectHandlerRejectsNothing(t *testing.T) {
	if _, err := ConnectHandler(context.Background(), "", &stubHandler{}); err == nil {
		t.Error("an unnamed server was accepted")
	}
	if _, err := ConnectHandler(context.Background(), "lalter", nil); err == nil {
		t.Error("a nil handler was accepted")
	}
}
