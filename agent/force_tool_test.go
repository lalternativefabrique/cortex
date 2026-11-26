package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAForcedToolIsSentOnlyWhenOffered(t *testing.T) {
	c := &httpClient{provider: Provider{Model: "m"}}
	tool := &fakeLeakTool{}
	forced, err := c.buildPayload(CompletionRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, Tools: []Tool{tool}, ForceTool: tool.Name()})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(forced)
	if !strings.Contains(string(body), `"tool_choice":{"type":"function","function":{"name":"`+tool.Name()+`"}}`) {
		t.Fatalf("payload = %s", body)
	}
	absent, err := c.buildPayload(CompletionRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}, ForceTool: "respond"})
	if err != nil {
		t.Fatal(err)
	}
	if absent.ToolChoice != nil {
		t.Fatal("a tool that is not offered cannot be forced")
	}
}
