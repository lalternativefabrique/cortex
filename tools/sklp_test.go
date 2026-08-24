package tools

import (
	"context"
	"testing"
)

func TestSklpInspectionRunsUnprompted(t *testing.T) {
	for _, cmd := range []string{
		"sklp issue list",
		"sklp issue show 47b1cfaa",
		"sklp deploy logs core -f",
		"sklp cache status",
		"sklp flow start fix-the-thing",
	} {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("%q should run without prompting", cmd)
		}
	}
}

func TestPushingAsksInEveryMode(t *testing.T) {
	// A pull request is public and carries the operator's name: waiving
	// approval for the workspace is not waiving it for what leaves the
	// machine.
	asked := &countingApprover{}
	for _, mode := range []Mode{ModeAsk, ModeAuto, ModeYes, ModePlan} {
		g := &GatedApprover{Mode: mode, Ask: asked}
		for _, cmd := range []string{"sklp flow end", "git push origin main", "gh pr merge 893"} {
			decision, _ := g.Approve(context.Background(), Request{Tool: "bash", Action: cmd})
			if decision != Allow {
				t.Errorf("mode %v: %q was refused rather than asked", mode, cmd)
			}
		}
	}
	if asked.calls != 12 {
		t.Fatalf("%d questions asked, want one per command per mode", asked.calls)
	}
}

func TestABroadAllowDoesNotCoverAPush(t *testing.T) {
	asked := &countingApprover{}
	g := &GatedApprover{Mode: ModeAuto, Ask: asked, Rules: Rules{Allow: []string{"sklp"}}}
	if _, err := g.Approve(context.Background(), Request{Tool: "bash", Action: "sklp flow end"}); err != nil {
		t.Fatal(err)
	}
	if asked.calls != 1 {
		t.Fatal("an allow rule let a push through without asking")
	}
}

type countingApprover struct{ calls int }

func (c *countingApprover) Approve(context.Context, Request) (Decision, error) {
	c.calls++
	return Allow, nil
}

func TestInspectionIsNotShadowedByItsFamily(t *testing.T) {
	// alwaysAsk holds "sklp deploy" and "sklp dev", and it is checked before
	// the allow rules — so a prefix match would swallow the reading forms
	// listed as inspections and prompt for every log tail.
	asked := &countingApprover{}
	g := &GatedApprover{Mode: ModeAuto, Ask: asked}
	for _, cmd := range []string{
		"sklp deploy logs core",
		"sklp deploy ls",
		"sklp dev --validate",
	} {
		if _, err := g.Approve(context.Background(), Request{Tool: "bash", Action: cmd}); err != nil {
			t.Fatalf("%q: %v", cmd, err)
		}
	}
	if asked.calls != 0 {
		t.Fatalf("%d question(s) asked for inspections that should run", asked.calls)
	}
}

func TestStartingTheLocalStackAsks(t *testing.T) {
	asked := &countingApprover{}
	g := &GatedApprover{Mode: ModeYes, Ask: asked}
	for _, cmd := range []string{"sklp dev core", "sklp dev down", "sklp run ci"} {
		if _, err := g.Approve(context.Background(), Request{Tool: "bash", Action: cmd}); err != nil {
			t.Fatalf("%q: %v", cmd, err)
		}
	}
	if asked.calls != 3 {
		t.Fatalf("%d asked, want one per command", asked.calls)
	}
}
