package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingApprover struct {
	asked    int
	decision Decision
}

func (c *countingApprover) Approve(context.Context, Request) (Decision, error) {
	c.asked++
	return c.decision, nil
}

func decide(t *testing.T, a Approver, req Request) Decision {
	t.Helper()
	d, err := a.Approve(context.Background(), req)
	var reason Refusal
	if err != nil && !errors.As(err, &reason) {
		t.Fatal(err)
	}
	return d
}

func refusalFor(t *testing.T, a Approver, req Request) Refusal {
	t.Helper()
	_, err := a.Approve(context.Background(), req)
	var reason Refusal
	if !errors.As(err, &reason) {
		t.Fatalf("denial carried no reason: %v", err)
	}
	return reason
}

func bashReq(line string) Request {
	return Request{Tool: "bash", Action: line, Scope: "bash:" + commandScope(line)}
}

func pathReq(tool, root, rel string) Request {
	abs := filepath.Join(root, rel)
	return Request{Tool: tool, Action: tool + " " + rel, Scope: tool + ":" + abs}
}

func rules(t *testing.T, allow, ask, deny []string) Rules {
	t.Helper()
	var r Rules
	var err error
	if r.Allow, err = ParseRules(allow); err != nil {
		t.Fatal(err)
	}
	if r.Ask, err = ParseRules(ask); err != nil {
		t.Fatal(err)
	}
	if r.Deny, err = ParseRules(deny); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestARuleIsParsedAsToolAndPattern(t *testing.T) {
	cases := map[string]Rule{
		"bash(git diff *)":   {Tool: "bash", Pattern: "git diff *"},
		"read(./.env)":       {Tool: "read", Pattern: "./.env"},
		"fetch_url":          {Tool: "fetch_url", Pattern: ""},
		" write( ./tmp/** )": {Tool: "write", Pattern: "./tmp/**"},
	}
	for in, want := range cases {
		got, err := ParseRule(in)
		if err != nil || got != want {
			t.Errorf("ParseRule(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "Bash(ls)", "bash(ls", "bash()", "git diff"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) accepted", bad)
		}
	}
}

func TestACommandPatternMatchesOnWordBoundaries(t *testing.T) {
	r := Rule{Tool: "bash", Pattern: "git diff *"}
	if !r.Matches(bashReq("git  diff HEAD~1"), "") {
		t.Error("a prefix pattern did not match, or whitespace was not normalised")
	}
	if r.Matches(bashReq("git diffs"), "") {
		t.Error("\"git diff *\" matched \"git diffs\"")
	}
	exact := Rule{Tool: "bash", Pattern: "go test"}
	if exact.Matches(bashReq("go test ./..."), "") || !exact.Matches(bashReq("go test"), "") {
		t.Error("a pattern without a trailing * must match the exact command only")
	}
}

func TestAPathPatternIsReadAgainstTheRoot(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		pattern, rel string
		want         bool
	}{
		{"./.env", ".env", true},
		{".env", ".env", true},
		{"./.env", "sub/.env", false},
		{"**/.env", "sub/.env", true},
		{"./secrets/**", "secrets/a/b.pem", true},
		{"**/*_test.go", "pkg/x_test.go", true},
		{"**/*_test.go", "pkg/x.go", false},
	}
	for _, c := range cases {
		r := Rule{Tool: "write", Pattern: c.pattern}
		if got := r.Matches(pathReq("write", root, c.rel), root); got != c.want {
			t.Errorf("%s against %s = %v, want %v", c.pattern, c.rel, got, c.want)
		}
	}
}

func TestAutoModeAllowsReadOnlyCommandsAndAsksForTheRest(t *testing.T) {
	ask := &countingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAuto, Ask: ask}
	for _, line := range []string{"ls -la", "git status", "git diff HEAD", "go vet ./...", "git status | head -20"} {
		if decide(t, g, bashReq(line)) != Allow {
			t.Errorf("%q was not allowed in auto mode", line)
		}
	}
	if ask.asked != 0 {
		t.Fatalf("the operator was prompted %d times for read-only commands", ask.asked)
	}
	for _, line := range []string{"rm file.go", "go build -o out ./...", "cat a.go > b.go", "ls | tee out.txt", "lsof -i"} {
		if decide(t, g, bashReq(line)) != Deny {
			t.Errorf("%q bypassed the prompt in auto mode", line)
		}
	}
	if ask.asked != 5 {
		t.Fatalf("prompted %d times, want 5", ask.asked)
	}
}

func TestAllowRulesSkipThePromptAndDenyRulesBeatEverything(t *testing.T) {
	ask := &countingApprover{decision: Allow}
	g := &GatedApprover{Mode: ModeAsk, Rules: rules(t, []string{"bash(git *)", "bash(go test *)"}, nil, []string{"bash(git push *)"}), Ask: ask}
	if decide(t, g, bashReq("go test ./...")) != Allow || ask.asked != 0 {
		t.Fatal("an allow rule did not take effect")
	}
	if decide(t, g, bashReq("git push origin main")) != Deny {
		t.Fatal("deny must win over a broader allow")
	}
	if decide(t, g, bashReq("git status && git push")) != Deny {
		t.Fatal("a deny must catch every stage of a chained command")
	}
	if decide(t, g, bashReq("git status")) != Allow {
		t.Fatal("the narrow deny swallowed the broad allow")
	}
	yes := &GatedApprover{Mode: ModeYes, Rules: rules(t, nil, nil, []string{"bash(terraform apply *)"})}
	if refusalFor(t, yes, bashReq("terraform apply -auto-approve")) != RefusedByRule {
		t.Fatal("mode yes overrode an explicit deny rule")
	}
}

func TestDangerousCommandsAreRefusedEvenUnderYes(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes}
	for _, line := range []string{"rm -rf / --no-preserve-root", "git push --force origin main", "git reset --hard HEAD~5", "git status && git clean -fdx"} {
		if decide(t, g, bashReq(line)) != Deny {
			t.Errorf("%q was allowed under mode yes", line)
		}
	}
	if decide(t, g, bashReq("rm -rf build")) != Allow {
		t.Error("an ordinary command was refused under mode yes")
	}
}

func TestOutwardFacingCommandsAlwaysAsk(t *testing.T) {
	ask := &countingApprover{decision: Allow}
	g := &GatedApprover{Mode: ModeYes, Rules: rules(t, []string{"bash(git *)"}, []string{"bash(make release *)"}, nil), Ask: ask}
	decide(t, g, bashReq("git push origin main"))
	decide(t, g, bashReq("make release v2"))
	if ask.asked != 2 {
		t.Fatalf("an outward-facing command and an ask rule prompted %d times, want 2", ask.asked)
	}
	product := &GatedApprover{Mode: ModeAuto, AlwaysAsk: []string{"sklp deploy"}, Ask: ask}
	if decide(t, product, bashReq("sklp deploy apply prod")) != Allow || ask.asked != 3 {
		t.Fatal("a product's own always-ask prefix was not honoured")
	}
	unattended := &GatedApprover{Mode: ModeYes}
	if refusalFor(t, unattended, bashReq("git push")) != RefusedUnattended {
		t.Fatal("with nobody to ask, an outward-facing command must be refused")
	}
}

func TestPlanModeLooksButDoesNotTouch(t *testing.T) {
	root := t.TempDir()
	g := &GatedApprover{Mode: ModePlan, Root: root, Rules: rules(t, []string{"write(./tmp/**)"}, nil, nil)}
	if decide(t, g, bashReq("git log --oneline")) != Allow {
		t.Fatal("plan mode must let inspection through")
	}
	if refusalFor(t, g, bashReq("go build ./...")) != RefusedByMode {
		t.Fatal("plan mode let a build through")
	}
	if refusalFor(t, g, pathReq("write", root, "tmp/x")) != RefusedByMode {
		t.Fatal("an allow rule let a write through in plan mode")
	}
}

func TestPathRulesGateEditsAndReads(t *testing.T) {
	root := t.TempDir()
	ask := &countingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAsk, Root: root, Ask: ask, Rules: rules(t,
		[]string{"edit(**/*_test.go)"}, nil, []string{"read(./.env)", "write(./secrets/**)"})}
	if decide(t, g, pathReq("edit", root, "pkg/x_test.go")) != Allow || ask.asked != 0 {
		t.Fatal("an allowed path still prompted")
	}
	if decide(t, g, pathReq("edit", root, "pkg/x.go")) != Deny || ask.asked != 1 {
		t.Fatal("an unlisted path did not prompt")
	}
	if refusalFor(t, g, pathReq("write", root, "secrets/k.pem")) != RefusedByRule {
		t.Fatal("a denied path was not refused")
	}
	if refusalFor(t, g, pathReq("read", root, ".env")) != RefusedByRule {
		t.Fatal("a denied read was not refused")
	}
	if decide(t, g, pathReq("read", root, "main.go")) != Allow || ask.asked != 1 {
		t.Fatal("a read was prompted for")
	}
}

func TestOtherToolsMatchByName(t *testing.T) {
	ask := &countingApprover{decision: Deny}
	g := &GatedApprover{Mode: ModeAsk, Ask: ask, Rules: rules(t, []string{"web_search"}, nil, []string{"fetch_url"})}
	if decide(t, g, Request{Tool: "web_search", Action: `{"query":"x"}`}) != Allow {
		t.Fatal("a tool named without a pattern was not allowed")
	}
	if refusalFor(t, g, Request{Tool: "fetch_url", Action: `{"url":"https://x"}`}) != RefusedByRule {
		t.Fatal("a denied tool ran")
	}
	if decide(t, g, Request{Tool: "weather", Action: `{}`}) != Deny || ask.asked != 1 {
		t.Fatal("an unlisted tool did not prompt")
	}
}

func TestAHookCanRefuseAnAction(t *testing.T) {
	inner := &countingApprover{decision: Allow}
	a := WithHooks(inner, []Hook{
		{Matcher: "bash", Command: `case "$CORTEX_ACTION" in *rm*) echo "no rm here"; exit 2;; esac`},
		{Matcher: "write", Command: "exit 1"},
	}, t.TempDir())
	if r := refusalFor(t, a, bashReq("rm -r build")); r != "a hook refused this action: no rm here" {
		t.Fatalf("refusal = %q", r)
	}
	if decide(t, a, bashReq("go test ./...")) != Allow {
		t.Fatal("a hook that exits 0 blocked the action")
	}
	if decide(t, a, Request{Tool: "write", Scope: "write:/x"}) != Allow {
		t.Fatal("a hook that fails for its own reasons must not block")
	}
	if inner.asked != 2 {
		t.Fatalf("the approver behind the hooks was reached %d times, want 2", inner.asked)
	}
}

func TestReadHonoursADenyRule(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &GatedApprover{Mode: ModeYes, Root: root, Rules: rules(t, nil, nil, []string{"read(./.env)"})}
	read := NewRead(ReadConfig{Root: root, Approver: g})
	res, err := read.Execute(context.Background(), []byte(`{"path":".env"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Metadata["ok"] != false || res.Content == "" || !strings.Contains(res.Content, "refused") {
		t.Fatalf("a denied read answered: %+v", res)
	}
}

func TestOnlyListedCommandsRunEvenUnderYes(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Commands: []string{"go test", "git status"}}
	for line, want := range map[string]Decision{
		"go test ./...":               Allow,
		"go test ./... && git status": Allow,
		"go build ./...":              Deny,
		"go test ./... && curl x":     Deny,
		"rm -r -f /":                  Deny,
		"sh -c 'go test'":             Deny,
		"go test $(rm x)":             Deny,
	} {
		d, _ := g.Approve(t.Context(), Request{Tool: "bash", Action: line})
		if d != want {
			t.Errorf("%q: got %v, want %v", line, d, want)
		}
	}
}

func TestAnEmptyCommandListRefusesEveryCommand(t *testing.T) {
	g := &GatedApprover{Mode: ModeYes, Commands: []string{}}
	if d, _ := g.Approve(t.Context(), Request{Tool: "bash", Action: "ls"}); d != Deny {
		t.Fatalf("got %v", d)
	}
}

func TestReadOnlyDetectionRefusesCommandsThatWriteOrRun(t *testing.T) {
	for _, line := range []string{
		"env rm x", "awk 'BEGIN{system(\"rm x\")}'", "sed -n 'w out' f",
		"sort -o out f", "sort -oout f", "git diff --output=out", "rg --pre ./x y",
		"git branch -D main", "git remote add x y", "find . -fprintf out x",
	} {
		if isReadOnlyCommand(line) {
			t.Errorf("%q passed as read-only", line)
		}
	}
	for _, line := range []string{"grep -o x f", "ls -d .", "git branch", "git branch -a", "sort f", "git remote -v"} {
		if !isReadOnlyCommand(line) {
			t.Errorf("%q no longer passes as read-only", line)
		}
	}
}
