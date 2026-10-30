package session

import (
	"encoding/json"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
)

func toolCall(name, args string) agent.ToolCall {
	return agent.ToolCall{Name: name, Arguments: json.RawMessage(args)}
}

func TestASkillPromptShowsTheRequestNotTheSkill(t *testing.T) {
	// A skill folds its instructions into the first message; showing the top
	// of it would name the skill for every session that used one.
	content := `You are working under the "ux-designer" skill, whose instructions follow.

<skill>
a long body nobody wants to see in a listing
</skill>

The request: make the prompt clearer`
	if got := openingAsk(content); got != "make the prompt clearer" {
		t.Fatalf("openingAsk = %q, want the request", got)
	}
}

func TestASkillInvokedBareNamesItself(t *testing.T) {
	content := `You are working under the "ux-designer" skill, whose instructions follow.

<skill>
a body
</skill>
`
	if got := openingAsk(content); got != "skill ux-designer" {
		t.Fatalf("openingAsk = %q, want the skill named", got)
	}
}

func TestAnOrdinaryPromptIsUntouched(t *testing.T) {
	const ask = "why does the test hang on CI?"
	if got := openingAsk(ask); got != ask {
		t.Fatalf("openingAsk = %q, want it unchanged", got)
	}
}

func TestPathsAreShownRelativeToTheirSession(t *testing.T) {
	root := "/repo"
	if got := relativeTo(root, "/repo/apps/code/main.go"); got != "apps/code/main.go" {
		t.Fatalf("relativeTo = %q, want it shortened", got)
	}
	// A path outside the root is left alone: shortening it would produce a
	// climb of ".." that says less than the path did.
	if got := relativeTo(root, "/elsewhere/main.go"); got != "/elsewhere/main.go" {
		t.Fatalf("relativeTo = %q, want it kept", got)
	}
}

func TestOnlyWritesCountAsTouched(t *testing.T) {
	// Listing what a session read would name every file in the repository
	// and distinguish nothing.
	for _, name := range []string{"read", "grep", "glob", "bash"} {
		if got := writtenPath(toolCall(name, `{"path":"x.go"}`)); got != "" {
			t.Errorf("%s counted as a write: %q", name, got)
		}
	}
	for _, name := range []string{"edit", "write"} {
		if got := writtenPath(toolCall(name, `{"path":"x.go"}`)); got != "x.go" {
			t.Errorf("%s = %q, want the path", name, got)
		}
	}
}
