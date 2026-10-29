package agent

import (
	"context"
	"testing"
)

func TestPersonRequestIsTheLastThingThePersonSaid(t *testing.T) {
	ctx := withPersonRequest(context.Background(), []Message{
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "ok"},
		{Role: RoleUser, Content: "now fix the test"},
		{Role: RoleTool, Content: "output"},
	})
	if got := PersonRequest(ctx); got != "now fix the test" {
		t.Fatalf("PersonRequest = %q", got)
	}
	setPersonRequest(ctx, "stop, revert instead")
	if got := PersonRequest(ctx); got != "stop, revert instead" {
		t.Fatalf("a message written mid-turn did not replace the request: %q", got)
	}
	if PersonRequest(context.Background()) != "" {
		t.Fatal("outside a turn the request must be empty")
	}
}
