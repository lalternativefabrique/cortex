package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeYAML(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigMergesUserAndRepo(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	writeYAML(t, filepath.Join(cfgDir, "skode", "mcp.yaml"),
		"servers:\n  - name: github\n    command: mcp-github\n")

	root := t.TempDir()
	writeYAML(t, filepath.Join(root, ".ai", "mcp.yaml"),
		"servers:\n  - name: postgres\n    command: mcp-postgres\n    args: [--dsn, postgres://local]\n")

	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("got %d servers, want both files merged", len(cfg.Servers))
	}
	if cfg.Servers[1].Args[0] != "--dsn" {
		t.Fatalf("args were lost: %v", cfg.Servers[1].Args)
	}
}

func TestLoadConfigToleratesMissingFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatalf("absent config files should not be an error: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatal("servers appeared from nowhere")
	}
}

func TestLoadConfigRejectsServerWithoutName(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	writeYAML(t, filepath.Join(root, ".ai", "mcp.yaml"), "servers:\n  - command: something\n")

	_, err := LoadConfig(root)
	if err == nil {
		t.Fatal("a nameless server was accepted, but names prefix its tools")
	}
	if !strings.Contains(err.Error(), "no name") {
		t.Fatalf("error does not say what is wrong: %v", err)
	}
}

func TestLoadConfigRejectsServerWithoutCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	writeYAML(t, filepath.Join(root, ".ai", "mcp.yaml"), "servers:\n  - name: orphan\n")

	if _, err := LoadConfig(root); err == nil {
		t.Fatal("a server with nothing to launch was accepted")
	}
}

func TestLoadConfigReportsMalformedYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	writeYAML(t, filepath.Join(root, ".ai", "mcp.yaml"), "servers: [unclosed")

	if _, err := LoadConfig(root); err == nil {
		t.Fatal("a malformed config was silently ignored")
	}
}

func TestStartSkipsUnreachableServer(t *testing.T) {
	var reported []error
	session, tools, err := Start(t.Context(), Config{Servers: []ServerConfig{
		{Name: "broken", Command: "/nonexistent/server", Timeout: time.Second},
		{Name: "fake", Command: fakeServer(t), Timeout: 10 * time.Second},
	}}, func(e error) { reported = append(reported, e) })
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if len(reported) != 1 {
		t.Fatalf("reported %d failures, want 1", len(reported))
	}
	if len(tools) == 0 {
		t.Fatal("one bad server suppressed the tools of a working one")
	}
	if got := session.Servers(); len(got) != 1 || got[0] != "fake" {
		t.Fatalf("connected servers = %v", got)
	}
}

func TestStartWithNoServers(t *testing.T) {
	session, tools, err := Start(t.Context(), Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if len(tools) != 0 {
		t.Fatal("tools appeared with no servers configured")
	}
}
