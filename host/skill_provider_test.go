package host

import (
	"context"
	"strings"
	"testing"

	"github.com/lalternativefabrique/cortex/agent"
)

func TestASkillOnAnotherModelRunsOnTheProviderThatServesIt(t *testing.T) {
	primary, primaryBodies := scriptedModel(t, scriptedReply{text: "primary"})
	other, otherBodies := scriptedModel(t, scriptedReply{text: "other"})
	var asked string
	srv := start(t, Config{
		Agent:    Agent{Name: "partage"},
		Provider: primary,
		Token:    "t",
		Skills:   []Skill{{ID: "write", Model: "mistral-medium"}},
		ProviderFor: func(_ context.Context, model string) (agent.Provider, bool) {
			asked = model
			other.Model = model
			return other, true
		},
	})
	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("write", "ctx-skill-provider", map[string]any{SkillKey: "write"})))
	if finalState(events) != "completed" {
		t.Fatalf("state = %q", finalState(events))
	}
	if asked != "mistral-medium" {
		t.Errorf("ProviderFor was asked for %q, want the skill's model", asked)
	}
	if len(*primaryBodies) != 0 || len(*otherBodies) != 1 || !strings.Contains((*otherBodies)[0], `"model":"mistral-medium"`) {
		t.Errorf("primary calls %d, other calls %v: the skill must run on the provider that serves its model", len(*primaryBodies), *otherBodies)
	}
}

func TestASkillOnAModelNoOtherProviderServesStaysOnTheAgentsProvider(t *testing.T) {
	primary, bodies := scriptedModel(t, scriptedReply{text: "primary"})
	srv := start(t, Config{
		Agent:       Agent{Name: "partage"},
		Provider:    primary,
		Token:       "t",
		Skills:      []Skill{{ID: "write", Model: "m2"}},
		ProviderFor: func(context.Context, string) (agent.Provider, bool) { return agent.Provider{}, false },
	})
	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("write", "ctx-skill-same", map[string]any{SkillKey: "write"})))
	if finalState(events) != "completed" || len(*bodies) != 1 || !strings.Contains((*bodies)[0], `"model":"m2"`) {
		t.Errorf("state %q, calls %v: the agent's provider runs the skill's model", finalState(events), *bodies)
	}
}

func TestARunOnTheAgentsOwnModelNeverAsksForAnotherProvider(t *testing.T) {
	primary, bodies := scriptedModel(t, scriptedReply{text: "primary"})
	called := false
	srv := start(t, Config{
		Agent:    Agent{Name: "partage"},
		Provider: primary,
		Token:    "t",
		ProviderFor: func(context.Context, string) (agent.Provider, bool) {
			called = true
			return agent.Provider{}, false
		},
	})
	events := readStream(t, rpc(t, srv.URL, "t", "message/stream", message("hi", "ctx-own", nil)))
	if finalState(events) != "completed" || len(*bodies) != 1 || called {
		t.Errorf("state %q, calls %d, ProviderFor asked %v", finalState(events), len(*bodies), called)
	}
}
