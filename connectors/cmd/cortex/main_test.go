package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalternative/packages/go/cortex/host"
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

func TestCallersAreOptionalButNeedAnAudience(t *testing.T) {
	if v, err := callersFromEnv(); v != nil || err != nil {
		t.Fatalf("unset = %v, %v", v != nil, err)
	}
	t.Setenv("CORTEX_ISSUER_URL", "lalter")
	if _, err := callersFromEnv(); err == nil {
		t.Fatal("an issuer without an audience must be refused")
	}
	t.Setenv("CORTEX_AUDIENCE", "cerveau")
	t.Setenv("CORTEX_JWKS_URL", "http://core.internal/api/v1/agent/jwks.json")
	if v, err := callersFromEnv(); v == nil || err != nil {
		t.Fatalf("configured = %v, %v", v != nil, err)
	}
}

func TestLentTokenReplacesTheConfiguredKey(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	client := &http.Client{Transport: lentToken{}}
	for _, ctx := range []context.Context{context.Background(), host.WithTurnToken(context.Background(), "turn-jwt")} {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
		req.Header.Set("Authorization", "Bearer configured-key")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	if !reflect.DeepEqual(got, []string{"Bearer configured-key", "Bearer turn-jwt"}) {
		t.Fatalf("got %q", got)
	}
}

func TestAnAgentNeedsATokenOrAnIssuer(t *testing.T) {
	t.Setenv("CORTEX_AGENT_NAME", "cerveau")
	t.Setenv("CORTEX_BASE_URL", "http://core/llm")
	if _, _, err := configFromEnv(); err == nil {
		t.Fatal("an agent anyone may call must be refused")
	}
	t.Setenv("CORTEX_ISSUER_URL", "lalter")
	t.Setenv("CORTEX_AUDIENCE", "cerveau")
	cfg, _, err := configFromEnv()
	if err != nil || cfg.Verify == nil {
		t.Fatalf("got verify=%v, %v", cfg.Verify != nil, err)
	}
}

func TestTheAgentReadsTheWebThroughTornadWhenItIsNamed(t *testing.T) {
	t.Setenv("CORTEX_TORNAD_URL", "http://tornad.internal/")
	t.Setenv("CORTEX_TORNAD_KEY", "k")
	got, err := webToolsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range got {
		names = append(names, tool.Name())
	}
	if !reflect.DeepEqual(names, []string{"web_search", "fetch_url"}) {
		t.Errorf("tools = %v", names)
	}
}

func TestWithoutTornadTheAgentHasNoWebOfItsOwn(t *testing.T) {
	t.Setenv("CORTEX_TORNAD_URL", "")
	got, err := webToolsFromEnv()
	if err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}

const catalogJSON = `{"instructions":"Never use an anglicism.","skills":[{"id":"find_sources","name":"Sources","description":"Find sources.","instructions":"You search.","input":{"type":"object"},"output":{"type":"object"}}]}`

func TestSkillsLoadFromTheAppOnceItAnswers(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(catalogJSON))
	}))
	defer srv.Close()

	var got host.Catalog
	done := make(chan struct{})
	go func() {
		loadSkillsFromURL(context.Background(), srv.URL, func(c host.Catalog) error { got = c; return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the skills were never loaded")
	}
	if len(got.Skills) != 1 || got.Skills[0].ID != "find_sources" || got.Instructions != "Never use an anglicism." || calls.Load() != 2 {
		t.Errorf("got %+v after %d calls", got, calls.Load())
	}
}

func TestLoadingStopsWithTheAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loadSkillsFromURL(ctx, srv.URL, func(host.Catalog) error { t.Error("set on a failing app"); return nil })
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("loading did not stop with the agent")
	}
}

func TestSkillsLoadFromAFile(t *testing.T) {
	path := t.TempDir() + "/skills.json"
	if err := os.WriteFile(path, []byte(catalogJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORTEX_SKILLS_FILE", path)
	got, err := skillsFromFile()
	if err != nil || len(got.Skills) != 1 || got.Instructions == "" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestWithoutAFileTheAgentStartsWithNoSkill(t *testing.T) {
	t.Setenv("CORTEX_SKILLS_FILE", "")
	got, err := skillsFromFile()
	if err != nil || got.Skills != nil || got.Instructions != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
