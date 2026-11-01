package tools

import (
	"context"
	"os"
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
	// Root is where a command's relative paths and globs resolve.
	Root string
}

func (s SecretGuard) Approve(ctx context.Context, req Request) (Decision, error) {
	if why := secretExposure(req, s.Root); why != "" {
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

func secretExposure(req Request, root string) string {
	for _, prefix := range []string{"read:", "write:", "edit:"} {
		if path, ok := strings.CutPrefix(req.Scope, prefix); ok && isSecretPath(path) {
			return "reaches " + filepath.Base(path)
		}
	}
	if req.Tool != "bash" {
		return ""
	}
	stages, err := parseShell(req.Action)
	if err != nil {
		stages = []stage{{Words: strings.Fields(req.Action)}}
	}
	for _, st := range stages {
		st = st.Command()
		line := st.String()
		if line == "env" || line == "set" || line == "export -p" || line == "export" {
			return "prints the environment"
		}
		for _, c := range secretCommands {
			if line == c || strings.HasPrefix(line, c+" ") {
				return "runs " + c
			}
		}
		for i, f := range st.Words {
			if isSecretPath(strings.Trim(f, `"'<>`)) {
				return "names " + f
			}
			if len(st.Globs) > i && st.Globs[i] && globReachesSecret(f, root) {
				return "expands " + f + " to a secret"
			}
		}
		for _, f := range st.Reads {
			if isSecretPath(f) {
				return "reads " + f
			}
		}
	}
	return ""
}

// globReachesSecret expands a pattern the way the shell would, against the
// root or the home, and reports a match that is a secret file.
func globReachesSecret(pattern, root string) bool {
	if strings.HasPrefix(pattern, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		pattern = filepath.Join(home, pattern[2:])
	} else if !filepath.IsAbs(pattern) {
		if root == "" {
			return false
		}
		pattern = filepath.Join(root, pattern)
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return false
	}
	for _, m := range matches {
		if isSecretPath(m) {
			return true
		}
	}
	return false
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
