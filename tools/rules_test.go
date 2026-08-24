package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// recordingApprover stands in for the operator prompt and records whether it
// was reached at all.
type recordingApprover struct {
	asked    int
	decision Decision
}

func (r *recordingApprover) Approve(context.Context, Request) (Decision, error) {
	r.asked++
	return r.decision, nil
}

// decide returns the decision, tolerating the Refusal that accompanies a
// denial. Only a non-Refusal error means the gate itself broke.
func decide(t *testing.T, g *GatedApprover, req Request) Decision {
	t.Helper()
	d, err := g.Approve(context.Background(), req)
	var reason Refusal
	if err != nil && !errors.As(err, &reason) {
		t.Fatal(err)
	}
	return d
}

// refusalFor returns why a request was denied.
func refusalFor(t *testing.T, g *GatedApprover, req Request) Refusal {
	t.Helper()
	_, err := g.Approve(context.Background(), req)
	var reason Refusal
	if !errors.As(err, &reason) {
		t.Fatalf("denial carried no reason: %v", err)
	}
	return reason
}

func bashReq(line string) Request {
	return Request{Tool: "bash", Action: line, Scope: "bash:" + commandScope(line)}
}

func TestAutoModeAllowsReadOnlyCommands(t *testing.T) {
	ask := &recordingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAuto, Ask: ask}

	for _, line := range []string{"ls -la", "git status", "git diff HEAD", "go vet ./..."} {
		if got := decide(t, g, bashReq(line)); got != Allow {
			t.Fatalf("%q was not allowed in auto mode", line)
		}
	}
	if ask.asked != 0 {
		t.Fatalf("the operator was prompted %d times for read-only commands", ask.asked)
	}
}

func TestAutoModeStillAsksForMutatingCommands(t *testing.T) {
	ask := &recordingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAuto, Ask: ask}

	for _, line := range []string{"git push", "rm file.go", "go build -o out ./..."} {
		if got := decide(t, g, bashReq(line)); got != Deny {
			t.Fatalf("%q bypassed the prompt in auto mode", line)
		}
	}
	if ask.asked != 3 {
		t.Fatalf("prompted %d times, want 3", ask.asked)
	}
}

func TestAutoModeDistinguishesGitSubcommands(t *testing.T) {
	ask := &recordingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAuto, Ask: ask}

	if decide(t, g, bashReq("git status")) != Allow {
		t.Fatal("git status should not prompt")
	}
	if decide(t, g, bashReq("git push origin main")) != Deny {
		t.Fatal("allowing git status must not allow git push")
	}
}

func TestReadOnlyMatchOnlyOnWordBoundary(t *testing.T) {
	g := &GatedApprover{Mode: ModeAuto, Ask: &recordingApprover{decision: Deny}}
	if decide(t, g, bashReq("lsof -i")) == Allow {
		t.Fatal("\"ls\" matched the start of \"lsof\"")
	}
}

func TestAutoModeRefusesRedirection(t *testing.T) {
	g := &GatedApprover{Mode: ModeAuto, Ask: &recordingApprover{decision: Deny}}
	if decide(t, g, bashReq("cat a.go > b.go")) == Allow {
		t.Fatal("a redirection writes a file, whatever produced the bytes")
	}
}

func TestAutoModeJudgesEveryPipelineStage(t *testing.T) {
	g := &GatedApprover{Mode: ModeAuto, Ask: &recordingApprover{decision: Deny}}
	if decide(t, g, bashReq("ls | tee out.txt")) == Allow {
		t.Fatal("a pipeline is only as safe as its most dangerous stage")
	}
	if decide(t, g, bashReq("git log && rm -rf build")) == Allow {
		t.Fatal("a chained mutating command was allowed")
	}
	if decide(t, g, bashReq("git status | head -20")) != Allow {
		t.Fatal("a pipeline of read-only stages should still pass")
	}
}

func TestAllowRuleSkipsPrompt(t *testing.T) {
	ask := &recordingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAsk, Rules: Rules{Allow: []string{"go test"}}, Ask: ask}

	if decide(t, g, bashReq("go test ./...")) != Allow {
		t.Fatal("an allow rule did not take effect")
	}
	if ask.asked != 0 {
		t.Fatal("the operator was prompted despite a matching allow rule")
	}
}

func TestDenyRuleBeatsAllowRule(t *testing.T) {
	g := &GatedApprover{
		Mode:  ModeAsk,
		Rules: Rules{Allow: []string{"git"}, Deny: []string{"git push"}},
		Ask:   &recordingApprover{decision: Allow},
	}
	if decide(t, g, bashReq("git push origin main")) != Deny {
		t.Fatal("deny must win over a broader allow")
	}
	if decide(t, g, bashReq("git status")) != Allow {
		t.Fatal("the narrow deny swallowed the broad allow")
	}
}

func TestDenyRuleBeatsYesMode(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Rules: Rules{Deny: []string{"terraform apply"}}}
	if decide(t, g, bashReq("terraform apply -auto-approve")) != Deny {
		t.Fatal("-mode=yes overrode an explicit deny rule")
	}
}

func TestDangerousCommandsRefusedEvenUnderYes(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes}
	for _, line := range []string{
		"rm -rf / --no-preserve-root",
		"git push --force origin main",
		"git reset --hard HEAD~5",
	} {
		if decide(t, g, bashReq(line)) != Deny {
			t.Fatalf("%q was allowed under -mode=yes", line)
		}
	}
}

func TestPlanModeRefusesEverything(t *testing.T) {
	g := &GatedApprover{Mode: ModePlan, Ask: &recordingApprover{decision: Allow}}
	if decide(t, g, bashReq("go build ./...")) != Deny {
		t.Fatal("plan mode allowed a command")
	}
	if decide(t, g, Request{Tool: "edit", Scope: "edit:/w/a.go"}) != Deny {
		t.Fatal("plan mode allowed an edit")
	}
}

func TestYesModeAllowsOrdinaryCommands(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes}
	if decide(t, g, bashReq("go build ./...")) != Allow {
		t.Fatal("-mode=yes prompted or refused an ordinary command")
	}
}

func TestWriteGlobsGateEdits(t *testing.T) {
	ask := &recordingApprover{decision: Deny}
	g := &GatedApprover{
		Mode:  ModeAsk,
		Root:  "/w",
		Rules: Rules{AllowWrite: []string{"**/*_test.go"}, DenyWrite: []string{"**/secrets.yaml"}},
		Ask:   ask,
	}

	if decide(t, g, Request{Tool: "edit", Scope: "edit:/w/pkg/a_test.go"}) != Allow {
		t.Fatal("an allow_write glob did not take effect")
	}
	if decide(t, g, Request{Tool: "write", Scope: "write:/w/config/secrets.yaml"}) != Deny {
		t.Fatal("a deny_write glob did not take effect")
	}
	if decide(t, g, Request{Tool: "edit", Scope: "edit:/w/pkg/a.go"}) != Deny {
		t.Fatal("an unlisted path should have reached the prompt")
	}
}

func TestDenyWriteBeatsYesMode(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Root: "/w", Rules: Rules{DenyWrite: []string{"**/*.env"}}}
	if decide(t, g, Request{Tool: "write", Scope: "write:/w/prod.env"}) != Deny {
		t.Fatal("-mode=yes overrode deny_write")
	}
}

func TestMissingAskDenies(t *testing.T) {
	g := &GatedApprover{Mode: ModeAsk}
	if decide(t, g, bashReq("go build ./...")) != Deny {
		t.Fatal("with nobody able to answer, the safe reading is deny")
	}
}

func TestLoadRulesMergesUserAndRepo(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := os.MkdirAll(filepath.Join(cfg, "skode"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cfg, "skode", "permissions.yaml"),
		[]byte("allow:\n  - go test\ndeny:\n  - git push\n"), 0o644)

	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".ai"), 0o755)
	os.WriteFile(filepath.Join(root, ".ai", "permissions.yaml"),
		[]byte("allow:\n  - make build\n"), 0o644)

	rules, err := LoadRules(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Allow) != 2 {
		t.Fatalf("Allow = %v, want both files merged", rules.Allow)
	}
	if len(rules.Deny) != 1 {
		t.Fatalf("Deny = %v, want the user rule preserved", rules.Deny)
	}
}

func TestLoadRulesToleratesMissingFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	rules, err := LoadRules(t.TempDir())
	if err != nil {
		t.Fatalf("absent permission files should not be an error: %v", err)
	}
	if len(rules.Allow) != 0 {
		t.Fatal("rules appeared from nowhere")
	}
}

func TestLoadRulesReportsMalformedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".ai"), 0o755)
	os.WriteFile(filepath.Join(root, ".ai", "permissions.yaml"), []byte("allow: [unclosed"), 0o644)

	if _, err := LoadRules(root); err == nil {
		t.Fatal("a malformed permissions file was silently ignored")
	}
}

func TestParseModeRejectsUnknown(t *testing.T) {
	if _, err := ParseMode("wideopen"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
	for _, m := range []string{"ask", "auto", "yes", "plan"} {
		if _, err := ParseMode(m); err != nil {
			t.Fatalf("ParseMode(%q) failed: %v", m, err)
		}
	}
}

func TestNormalizeCommandCollapsesWhitespace(t *testing.T) {
	g := &GatedApprover{Mode: ModeAsk, Rules: Rules{Allow: []string{"go test"}}, Ask: &recordingApprover{decision: Deny}}
	if decide(t, g, bashReq("go   test    ./...")) != Allow {
		t.Fatal("odd spacing defeated a rule that should match")
	}
}

func TestRefusalExplainsItself(t *testing.T) {
	rule := &GatedApprover{Mode: ModeAsk, Rules: Rules{Deny: []string{"git push"}}}
	if got := refusalFor(t, rule, bashReq("git push")); got != RefusedByRule {
		t.Fatalf("refusal = %q, want the rule reason", got)
	}

	plan := &GatedApprover{Mode: ModePlan}
	if got := refusalFor(t, plan, bashReq("go build ./...")); got != RefusedByMode {
		t.Fatalf("refusal = %q, want the read-only reason", got)
	}

	unattended := &GatedApprover{Mode: ModeAsk}
	if got := refusalFor(t, unattended, bashReq("go build ./...")); got != RefusedUnattended {
		t.Fatalf("refusal = %q, want the unattended reason", got)
	}
}
