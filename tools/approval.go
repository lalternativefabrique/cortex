package tools

import (
	"context"
	"errors"
)

// Decision is the outcome of an approval request.
type Decision int

const (
	// Deny rejects this single request.
	Deny Decision = iota
	// Allow permits this single request.
	Allow
	// AlwaysAllow permits this request and every later one matching the same
	// Request.Scope for the rest of the session.
	AlwaysAllow
)

// Refusal explains why an action was denied, so the model learns whether a
// person said no, a rule said no, or the run is read-only — three situations
// calling for different next moves. It implements error so it can travel
// with the Decision instead of being stashed on a shared approver, which
// concurrent tool calls would race on.
type Refusal string

func (r Refusal) Error() string { return string(r) }

const (
	RefusedByOperator Refusal = "the operator declined this action"
	RefusedByRule     Refusal = "a permission rule forbids this action"
	RefusedByMode     Refusal = "this run is read-only, so nothing may be changed"
	RefusedUnattended Refusal = "nobody is available to approve this action"
)

// Request describes an action awaiting approval.
type Request struct {
	// Tool is the tool asking.
	Tool string
	// Action is a one-line rendering of what will happen, shown to the
	// operator verbatim.
	Action string
	// Scope keys the AlwaysAllow memory. Commands scope by program name so
	// approving one `go test` approves the next; file writes scope by path.
	Scope string
	// Detail carries optional context, such as the diff about to be applied.
	Detail string
}

// Approver decides whether an action may proceed. Implementations must be
// safe for concurrent use: a single model turn can request several tools at
// once.
//
// A Deny may carry a Refusal as its error to explain itself; any other error
// means the decision could not be made at all.
//
// This gate is enforced here, in the tool handler, and never delegated to
// the system prompt: an instruction the model is asked to respect is not a
// control, since the model is exactly what the gate exists to constrain.
type Approver interface {
	Approve(ctx context.Context, req Request) (Decision, error)
}

// AllowAll approves everything. It suits non-interactive runs where the
// caller has already accepted the blast radius.
type AllowAll struct{}

func (AllowAll) Approve(context.Context, Request) (Decision, error) { return Allow, nil }

// DenyAll refuses everything, leaving the agent read-only.
type DenyAll struct{}

func (DenyAll) Approve(context.Context, Request) (Decision, error) { return Deny, RefusedByMode }

// approve runs the gate and renders the outcome for the model.
//
// A Refusal is information the model can act on, so it comes back as a tool
// result. Any other error means the gate itself failed, which is not
// something the model can work around.
func approve(ctx context.Context, a Approver, req Request) (refused string, err error) {
	decision, err := a.Approve(ctx, req)
	if decision == Allow || decision == AlwaysAllow {
		return "", nil
	}
	var reason Refusal
	if errors.As(err, &reason) {
		return string(reason), nil
	}
	if err != nil {
		return "", err
	}
	return string(RefusedByOperator), nil
}
