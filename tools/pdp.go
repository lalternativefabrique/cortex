package tools

import (
	"context"
	"maps"
	"strings"

	"github.com/lalternative/packages/go/authz"
)

// PDPApprover asks a policy decision point, in the AuthZEN shape, before
// every write or command. The product's rules live behind the PDP; nothing
// here knows what a command is allowed to be.
type PDPApprover struct {
	PDP     authz.PDP
	Subject authz.Subject
	// Product prefixes resource types, as in "lalter:command".
	Product string
	// Context is what the host knows about this run and the policy may
	// read: the workspace, the mode, the grant the domain already computed.
	Context map[string]any
	// Ask is consulted when the PDP answers approval_required. Nil means the
	// run is unattended and such actions are refused.
	Ask Approver
}

func (p *PDPApprover) Approve(ctx context.Context, req Request) (Decision, error) {
	d, err := p.PDP.Evaluate(ctx, p.request(req))
	if err != nil {
		return Deny, err
	}
	if d.Allow {
		return Allow, nil
	}
	if d.Reason == authz.ReasonApprovalRequired {
		if p.Ask == nil {
			return Deny, RefusedUnattended
		}
		return p.Ask.Approve(ctx, req)
	}
	return Deny, RefusedByRule
}

func (p *PDPApprover) request(req Request) authz.Request {
	c := map[string]any{}
	maps.Copy(c, p.Context)
	out := authz.Request{
		Subject: p.Subject,
		Action:  authz.Action{Name: req.Tool},
		Context: c,
	}
	if req.Tool == "bash" {
		out.Action.Properties = map[string]any{"command": req.Action}
		out.Resource = authz.Resource{Type: p.Product + ":command", ID: req.Action}
		return out
	}
	if req.Scope == "" {
		out.Action.Properties = map[string]any{"arguments": req.Action}
		out.Resource = authz.Resource{Type: p.Product + ":tool", ID: req.Tool}
		return out
	}
	out.Action.Properties = map[string]any{"description": req.Action}
	out.Resource = authz.Resource{Type: p.Product + ":path", ID: strings.TrimPrefix(req.Scope, req.Tool+":")}
	return out
}
