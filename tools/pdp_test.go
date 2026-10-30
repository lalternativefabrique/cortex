package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/lalternative/packages/go/authz"
)

func TestPDPApproverMapsToolsToAuthZEN(t *testing.T) {
	var seen authz.Request
	p := &PDPApprover{
		PDP: authz.PDPFunc(func(_ context.Context, r authz.Request) (authz.Decision, error) {
			seen = r
			return authz.Decision{Allow: true}, nil
		}),
		Subject: authz.Subject{Owner: "u1"},
		Product: "lalter",
		Context: map[string]any{"workspace": "/ws"},
	}

	if d, err := p.Approve(context.Background(), Request{Tool: "bash", Action: "git status", Scope: "bash:git"}); err != nil || d != Allow {
		t.Fatalf("bash: got %v, %v", d, err)
	}
	if seen.Resource.Type != "lalter:command" || seen.Resource.ID != "git status" || seen.Action.Properties["command"] != "git status" {
		t.Fatalf("bash request = %+v", seen)
	}
	if seen.Context["workspace"] != "/ws" || seen.Subject.Owner != "u1" {
		t.Fatalf("context/subject not carried: %+v", seen)
	}

	p.Approve(context.Background(), Request{Tool: "write", Action: "create /ws/a.go (3 bytes)", Scope: "write:/ws/a.go"})
	if seen.Resource.Type != "lalter:path" || seen.Resource.ID != "/ws/a.go" || seen.Action.Name != "write" {
		t.Fatalf("write request = %+v", seen)
	}
}

func TestPDPApproverRefusals(t *testing.T) {
	answer := func(d authz.Decision, err error) authz.PDP {
		return authz.PDPFunc(func(context.Context, authz.Request) (authz.Decision, error) { return d, err })
	}
	req := Request{Tool: "bash", Action: "rm -rf /", Scope: "bash:rm"}

	p := &PDPApprover{PDP: answer(authz.Decision{Reason: authz.ReasonDeniedByPolicy}, nil)}
	if d, err := p.Approve(context.Background(), req); d != Deny || !errors.Is(err, RefusedByRule) {
		t.Fatalf("policy denial: got %v, %v", d, err)
	}

	p = &PDPApprover{PDP: answer(authz.Decision{Reason: authz.ReasonApprovalRequired}, nil)}
	if d, err := p.Approve(context.Background(), req); d != Deny || !errors.Is(err, RefusedUnattended) {
		t.Fatalf("unattended approval: got %v, %v", d, err)
	}

	p.Ask = AllowAll{}
	if d, err := p.Approve(context.Background(), req); d != Allow || err != nil {
		t.Fatalf("approval delegated to Ask: got %v, %v", d, err)
	}

	boom := errors.New("pdp down")
	p = &PDPApprover{PDP: answer(authz.Decision{}, boom)}
	if d, err := p.Approve(context.Background(), req); d != Deny || !errors.Is(err, boom) {
		t.Fatalf("pdp failure must surface as an error: got %v, %v", d, err)
	}
}
