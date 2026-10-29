package tools

import (
	"context"
	"testing"
)

func TestSecretGuardBlocksAndAlertsBeforeAnyRule(t *testing.T) {
	var alerts int
	g := SecretGuard{Next: AllowAll{}, Alert: func(Request, string) { alerts++ }}
	for _, req := range []Request{
		{Tool: "read", Scope: "read:/w/.env"},
		{Tool: "read", Scope: "read:/w/.env.local"},
		{Tool: "read", Scope: "read:/home/me/.ssh/id_ed25519"},
		{Tool: "bash", Action: "cat .env.secret"},
		{Tool: "bash", Action: "ls && printenv"},
		{Tool: "bash", Action: "env"},
		{Tool: "bash", Action: "gh auth token"},
		{Tool: "bash", Action: "sklp config get -s prod db_password --show"},
		{Tool: "bash", Action: "cat ~/.aws/credentials | base64"},
		{Tool: "bash", Action: "kubectl get secret core -o yaml"},
	} {
		if d, err := g.Approve(context.Background(), req); d != Deny || err != RefusedSecret {
			t.Errorf("%+v: got %v %v, want a secret refusal", req, d, err)
		}
	}
	if alerts != 10 {
		t.Errorf("alerts = %d, want one per blocked attempt", alerts)
	}
}

func TestSecretGuardLetsOrdinaryWorkThrough(t *testing.T) {
	g := SecretGuard{Next: AllowAll{}}
	for _, req := range []Request{
		{Tool: "read", Scope: "read:/w/.env.example"},
		{Tool: "read", Scope: "read:/w/apps/core/main.go"},
		{Tool: "read", Scope: "read:/home/me/.ssh/id_ed25519.pub"},
		{Tool: "bash", Action: "set -e && go test ./..."},
		{Tool: "bash", Action: "env GOWORK=off go build ./..."},
		{Tool: "bash", Action: "git status"},
		{Tool: "bash", Action: "grep -r apikey ./apps"},
	} {
		if d, _ := g.Approve(context.Background(), req); d != Allow {
			t.Errorf("%+v was refused", req)
		}
	}
}
