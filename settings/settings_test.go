package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/tools"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheThreeFilesAddUpAndTheNearestModeWins(t *testing.T) {
	cfg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	write(t, filepath.Join(cfg, "ark", "settings.yaml"), "mode: auto\npermissions:\n  allow: [\"bash(go test *)\"]\n  deny: [\"read(~/.ssh/**)\"]\n")
	write(t, filepath.Join(root, ".ai", "settings.yaml"), "permissions:\n  allow: [\"bash(pnpm test *)\"]\n  ask: [\"bash(git push *)\"]\nhooks:\n  pre_tool_use:\n    - matcher: bash\n      command: ./.ai/hooks/check.sh\n")
	write(t, filepath.Join(root, ".ai", "settings.local.yaml"), "mode: yes\npermissions:\n  deny: [\"bash(sklp deploy *)\"]\n")

	s, err := Load("ark", root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != tools.ModeYes || !strings.HasSuffix(s.ModeOrigin, "settings.local.yaml") {
		t.Fatalf("mode = %s from %s", s.Mode, s.ModeOrigin)
	}
	if len(s.Allow) != 2 || len(s.Ask) != 1 || len(s.Deny) != 2 || len(s.PreToolUse) != 1 || len(s.Files) != 3 {
		t.Fatalf("settings = %+v", s)
	}
	if !strings.HasSuffix(s.Allow[0].Origin, filepath.Join("ark", "settings.yaml")) || !strings.HasSuffix(s.Deny[1].Origin, "settings.local.yaml") {
		t.Fatalf("origins = %s, %s", s.Allow[0].Origin, s.Deny[1].Origin)
	}
}

func TestAMissingFileIsNothingAndAMalformedOneIsNamed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	s, err := Load("ark", root)
	if err != nil || s.Mode != tools.ModeAsk || len(s.Files) != 0 {
		t.Fatalf("empty settings = %+v, %v", s, err)
	}
	write(t, filepath.Join(root, ".ai", "settings.yaml"), "permissions:\n  allow: [\"Bash(ls)\"]\n")
	if _, err := Load("ark", root); err == nil || !strings.Contains(err.Error(), "settings.yaml") {
		t.Fatalf("a bad rule was not reported with its file: %v", err)
	}
	write(t, filepath.Join(root, ".ai", "settings.yaml"), "mode: maybe\n")
	if _, err := Load("ark", root); err == nil || !strings.Contains(err.Error(), "maybe") {
		t.Fatalf("a bad mode was not reported: %v", err)
	}
}

func TestTheApproverEnforcesWhatTheFilesSay(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	write(t, filepath.Join(root, ".ai", "settings.yaml"), "mode: yes\npermissions:\n  deny: [\"write(./secrets/**)\"]\nhooks:\n  pre_tool_use:\n    - matcher: bash\n      command: 'case \"$CORTEX_ACTION\" in *curl*) exit 2;; esac'\n")
	s, err := Load("ark", root)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Approver(root, nil)
	if d, _ := a.Approve(t.Context(), tools.Request{Tool: "bash", Action: "go build ./..."}); d != tools.Allow {
		t.Fatal("mode yes did not allow an ordinary command")
	}
	if d, _ := a.Approve(t.Context(), tools.Request{Tool: "bash", Action: "curl https://x"}); d != tools.Deny {
		t.Fatal("the hook did not block")
	}
	if d, _ := a.Approve(t.Context(), tools.Request{Tool: "write", Scope: "write:" + filepath.Join(root, "secrets", "k")}); d != tools.Deny {
		t.Fatal("the deny rule did not hold under mode yes")
	}
}
