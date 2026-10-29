package tools

import (
	"strings"
	"testing"
)

const leakedValue = "l0ngNatsPassw0rdValue42xyz"

func TestRedactorMasksAValueReadFromASecretFileWhereverItShowsUp(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, ".env.secret", "NATS_GLOBAL_PASSWORD="+leakedValue+"\nPORT=4100\n")
	r := &SecretRedactor{Root: root}
	out, hit := r.Redact(".env.secret:4:NATS_GLOBAL_PASSWORD=" + leakedValue + "\nValue: " + leakedValue)
	if !hit || strings.Contains(out, leakedValue) {
		t.Fatalf("the value reached the output: %q", out)
	}
	if plain, hit := r.Redact("listening on 4100"); hit || plain != "listening on 4100" {
		t.Fatalf("a short ordinary value was masked: %q", plain)
	}
}

func TestRedactorMasksKeysAndTokensByTheirShape(t *testing.T) {
	r := &SecretRedactor{}
	for _, secret := range []string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk\n-----END OPENSSH PRIVATE KEY-----",
		"ghp_" + strings.Repeat("a", 36),
		"sk-" + strings.Repeat("b", 32),
		"AKIA" + strings.Repeat("C", 16),
		"eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4",
	} {
		if out, hit := r.Redact("token: " + secret); !hit || strings.Contains(out, secret) {
			t.Errorf("not masked: %q", out)
		}
	}
	out, _ := r.Redact("postgres://app:hunter2hunter2@db:5432/lalter")
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "postgres://app:") {
		t.Errorf("the password in a URL was not masked alone: %q", out)
	}
}

func TestRedactorMasksTheProcessCredentials(t *testing.T) {
	t.Setenv("LALTER_API_TOKEN", "process-token-value-123")
	out, hit := (&SecretRedactor{}).Redact("Authorization: Bearer process-token-value-123")
	if !hit || strings.Contains(out, "process-token-value-123") {
		t.Fatalf("a credential of the process reached the output: %q", out)
	}
}

func TestGrepNeverSearchesSecretFiles(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, ".env.secret", "NATS_PASSWORD="+leakedValue+"\n")
	writeTemp(t, root, "deploy.yaml", "password: ${NATS_PASSWORD}\n")
	out := run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "NATS_PASSWORD"})
	if strings.Contains(out, ".env.secret") || strings.Contains(out, leakedValue) {
		t.Fatalf("grep searched a secret file: %q", out)
	}
	withoutRipgrep(t)
	out = run(t, NewGrep(GrepConfig{Root: root}), map[string]any{"pattern": "NATS_PASSWORD"})
	if strings.Contains(out, ".env.secret") || !strings.Contains(out, "deploy.yaml") {
		t.Fatalf("the fallback search searched a secret file or missed the others: %q", out)
	}
}
