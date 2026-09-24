package main

import (
	"reflect"
	"testing"

	"github.com/lalternative/packages/go/cortex/mcp"
)

func TestMCPServersFromEnvReadsNamedURLs(t *testing.T) {
	got, err := mcpServersFromEnv(" synthiz=https://api.synthiz.com/mcp , local=http://127.0.0.1:4000/mcp,")
	if err != nil {
		t.Fatal(err)
	}
	want := []mcp.ServerConfig{
		{Name: "synthiz", URL: "https://api.synthiz.com/mcp"},
		{Name: "local", URL: "http://127.0.0.1:4000/mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMCPServersFromEnvIsEmptyWhenUnset(t *testing.T) {
	got, err := mcpServersFromEnv("")
	if err != nil || got != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestMCPServersFromEnvRefusesAnEntryItCannotReach(t *testing.T) {
	for _, raw := range []string{"https://api.synthiz.com/mcp", "=https://x/mcp", "synthiz=api.synthiz.com/mcp", "synthiz=/usr/bin/server"} {
		if _, err := mcpServersFromEnv(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestConfigFromEnvAddsTheServersOfTheEnvironment(t *testing.T) {
	t.Setenv("CORTEX_TOKEN", "t")
	t.Setenv("CORTEX_MCP_SERVERS", "synthiz=https://api.synthiz.com/mcp")
	cfg, _, err := configFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCP.Servers) != 1 || cfg.MCP.Servers[0].Name != "synthiz" {
		t.Fatalf("servers %+v", cfg.MCP.Servers)
	}
}
