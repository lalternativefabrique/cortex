package tools

import (
	"context"
	"path/filepath"
	"strings"
)

// RefusedSecret is an action that would read, print or hand over a secret.
// It is refused before any rule, mode or reviewer, and raised as an alert.
const RefusedSecret Refusal = "🔴 SECRET: this action would expose a secret (a key, a token, a password or an environment file); it was blocked and the person was alerted"

// SecretAlert is told of every blocked attempt, for the person to see.
type SecretAlert func(req Request, why string)

// SecretGuard refuses any action touching a secret before Next decides.
type SecretGuard struct {
	Next  Approver
	Alert SecretAlert
}

func (s SecretGuard) Approve(ctx context.Context, req Request) (Decision, error) {
	if why := secretExposure(req); why != "" {
		if s.Alert != nil {
			s.Alert(req, why)
		}
		return Deny, RefusedSecret
	}
	return s.Next.Approve(ctx, req)
}

// secretFiles are file names or path fragments that hold credentials.
var secretFiles = []string{
	".env", ".env.local", ".env.secret", ".env.prod", ".env.production",
	".ssh/id_", ".ssh/authorized_keys", ".netrc", ".pgpass", ".npmrc", ".pypirc",
	".aws/credentials", ".config/gh/hosts.yml", ".docker/config.json", ".kube/config",
	".git-credentials", ".config/sklp/config.yaml",
	".pem", ".key", ".p12", ".pfx", "id_rsa", "id_ed25519",
	"/proc/self/environ", "/environ",
}

// secretFileGlobs keep a content search out of the files that hold secrets.
var secretFileGlobs = []string{
	".env", ".env.*", "*.pem", "*.key", "*.p12", "*.pfx", "id_rsa*", "id_ed25519*",
	".netrc", ".pgpass", ".npmrc", ".pypirc", ".git-credentials",
}

// secretFilesFine are look-alikes that hold no secret.
var secretFilesFine = []string{".env.example", ".env.sample", ".env.template", ".pub", ".keys", ".key.go"}

// secretCommands print or fetch credentials.
var secretCommands = []string{
	"printenv",
	"gh auth token", "gh auth status --show-token",
	"sklp config get", "sklp secret pull", "sklp secret get",
	"secret-tool lookup", "security find-generic-password", "security find-internet-password",
	"pass show", "op read", "op item get", "bw get", "vault kv get", "vault read",
	"kubectl get secret", "kubectl describe secret",
	"aws configure get", "gcloud auth print-access-token", "gcloud auth print-identity-token",
	"docker login", "git credential fill",
}

func secretExposure(req Request) string {
	for _, prefix := range []string{"read:", "write:", "edit:"} {
		if path, ok := strings.CutPrefix(req.Scope, prefix); ok && isSecretPath(path) {
			return "reaches " + filepath.Base(path)
		}
	}
	if req.Tool != "bash" {
		return ""
	}
	for _, stage := range splitStages(req.Action) {
		stage = normalizeCommand(stage)
		if stage == "env" || stage == "set" || stage == "export -p" || stage == "export" {
			return "prints the environment"
		}
		for _, c := range secretCommands {
			if stage == c || strings.HasPrefix(stage, c+" ") {
				return "runs " + c
			}
		}
		for _, f := range strings.Fields(stage) {
			if isSecretPath(strings.Trim(f, `"'<>`)) {
				return "names " + f
			}
		}
	}
	return ""
}

func isSecretPath(path string) bool {
	for _, fine := range secretFilesFine {
		if strings.HasSuffix(path, fine) {
			return false
		}
	}
	base := filepath.Base(path)
	for _, s := range secretFiles {
		if strings.HasPrefix(s, ".") && !strings.Contains(s, "/") {
			if base == s || strings.HasPrefix(s, ".env") && strings.HasPrefix(base, s+".") || !strings.HasPrefix(s, ".env") && strings.HasSuffix(base, s) {
				return true
			}
			continue
		}
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}
