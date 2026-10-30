package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func toolNames(tools []Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	return names
}

func browserSet(click *fakeTool) Toolset {
	return Toolset{Name: "browser", Summary: "drive a web page", Instructions: "Snapshot first.", Tools: []Tool{click}}
}

func TestADeferredSetIsNotOfferedUntilLoaded(t *testing.T) {
	click := &fakeTool{name: "browser_click", result: "clicked"}
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "1", Name: LoadToolsName, Arguments: json.RawMessage(`{"set":"browser"}`)}}},
		{ToolCalls: []ToolCall{{ID: "2", Name: "browser_click", Arguments: json.RawMessage(`{}`)}}},
		{Text: "done"},
	}}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{&fakeTool{name: "read"}}, Deferred: []Toolset{browserSet(click)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "open the page"}}); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(toolNames(client.calls[0].Tools), ","); got != "read,load_tools" {
		t.Fatalf("first step offered %s, want read,load_tools", got)
	}
	if got := strings.Join(toolNames(client.calls[1].Tools), ","); got != "read,load_tools,browser_click" {
		t.Fatalf("after loading offered %s, want read,load_tools,browser_click", got)
	}
	if click.invoked != 1 {
		t.Fatalf("browser_click invoked %d times, want 1", click.invoked)
	}
	loaded := client.calls[1].Messages[len(client.calls[1].Messages)-1].Content
	if !strings.Contains(loaded, "browser_click") || !strings.Contains(loaded, "Snapshot first.") {
		t.Fatalf("load result = %q, want the tool names and the server's instructions", loaded)
	}
}

func TestALoadedSetStaysOfferedOnTheNextTurn(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "ok"}}}
	r, err := NewRunner(Config{Client: client, Deferred: []Toolset{browserSet(&fakeTool{name: "browser_click"})}})
	if err != nil {
		t.Fatal(err)
	}
	history := []Message{
		{Role: RoleUser, Content: "open it"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "1", Name: LoadToolsName, Arguments: json.RawMessage(`{"set":"browser"}`)}}},
		{Role: RoleTool, ToolCallID: "1", Content: "Loaded browser: browser_click"},
		{Role: RoleAssistant, Content: "opened"},
		{Role: RoleUser, Content: "click it"},
	}
	if _, err := r.Run(context.Background(), history); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(toolNames(client.calls[0].Tools), ","); got != "load_tools,browser_click" {
		t.Fatalf("offered %s, want load_tools,browser_click", got)
	}
}

func TestLoadingAnUnknownSetTellsTheModel(t *testing.T) {
	res, err := loadTools{sets: []Toolset{browserSet(&fakeTool{name: "browser_click"})}}.
		Execute(context.Background(), json.RawMessage(`{"set":"database"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, `no tool set named "database"`) {
		t.Fatalf("Content = %q", res.Content)
	}
}

func TestADeferredToolMayNotShadowAnOfferedOne(t *testing.T) {
	_, err := NewRunner(Config{
		Client:   &scriptedClient{},
		Tools:    []Tool{&fakeTool{name: "browser_click"}},
		Deferred: []Toolset{browserSet(&fakeTool{name: "browser_click"})},
	})
	if err == nil {
		t.Fatal("NewRunner accepted a deferred tool named like an offered one")
	}
}
