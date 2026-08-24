package tools

import (
	"context"
	"errors"
	"testing"
)

func TestReadingVerbsRunUnprompted(t *testing.T) {
	for _, cmd := range []string{
		"kubectl get pods -n prod",
		"kubectl describe deployment core",
		"kubectl logs -f core-abc",
		"helm list -A",
		"docker ps -a",
	} {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("%q should run without prompting", cmd)
		}
	}
}

func TestWritingVerbsAreNotReadOnly(t *testing.T) {
	// A cluster is not the workspace: none of this is undone by a checkout.
	for _, cmd := range []string{
		"kubectl apply -f deploy.yaml",
		"kubectl delete pod core-abc",
		"kubectl scale deployment core --replicas=0",
		"kubectl rollout restart deployment/core",
		"helm upgrade core ./chart",
		"docker rm core",
	} {
		if isReadOnlyCommand(cmd) {
			t.Errorf("%q was treated as an inspection", cmd)
		}
	}
}

func TestAReadingVerbDoesNotCoverWhatFollowsIt(t *testing.T) {
	// The prefix match is per command line, so chaining must not smuggle a
	// write in behind an allowed prefix.
	for _, cmd := range []string{
		"kubectl get pods && kubectl delete pod core-abc",
		"kubectl get pods; kubectl apply -f x.yaml",
		"kubectl get pods | xargs kubectl delete pod",
	} {
		if isReadOnlyCommand(cmd) {
			t.Errorf("%q let a write through behind an allowed prefix", cmd)
		}
	}
}

func TestDestructiveClusterVerbsAreRefusedEverywhere(t *testing.T) {
	// Including under -mode yes: the operator waived approval for the
	// workspace, not for whatever the current context points at.
	for _, mode := range []Mode{ModeAsk, ModeAuto, ModeYes, ModePlan} {
		g := &GatedApprover{Mode: mode, Ask: AllowAll{}}
		for _, cmd := range []string{
			"kubectl delete namespace prod",
			"kubectl drain node-1",
			"helm uninstall core",
		} {
			decision, err := g.Approve(context.Background(), Request{Tool: "bash", Action: cmd})
			if decision != Deny {
				t.Errorf("mode %v allowed %q", mode, cmd)
			}
			if !errors.Is(err, RefusedByRule) {
				t.Errorf("mode %v refused %q for %v, want the rule", mode, cmd, err)
			}
		}
	}
}
