// Command cortex runs one declared agent in a container, configured by its
// environment, behind A2A (lalter ADR 0013).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
	lalterrecall "github.com/lalternative/packages/go/cortex/connectors/recall/lalter"
	"github.com/lalternative/packages/go/cortex/host"
	"github.com/lalternative/packages/go/cortex/mcp"
	"github.com/lalternative/packages/go/cortex/recall"
	"github.com/lalternative/packages/go/svcauth"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(turnHandler{slog.NewJSONHandler(os.Stdout, nil)}))
	if err := run(); err != nil {
		slog.Error("cortex", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, addr, err := configFromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry(ctx, cfg.Agent.Name)
	if err != nil {
		slog.Warn("cortex: skalpai unreachable, logging to stdout only", "error", err)
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTelemetry(flush)
	}()

	shipped, err := skillsFromFile()
	if err != nil {
		return err
	}
	cfg.Skills, cfg.AppInstructions = shipped.Skills, shipped.Instructions

	h, err := host.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer h.Close()

	srv := &http.Server{Addr: addr, Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if url := strings.TrimSpace(os.Getenv("CORTEX_SKILLS_URL")); url != "" {
		h.SetSkillSource(func(ctx context.Context) (host.Catalog, error) { return fetchSkills(ctx, url) })
		go loadSkillsFromURL(ctx, url, h.SetCatalog)
	} else if strings.TrimSpace(os.Getenv("CORTEX_SKILLS_FILE")) != "" {
		h.SetSkillSource(func(context.Context) (host.Catalog, error) { return skillsFromFile() })
	}
	slog.Info("cortex listening", "addr", addr, "agent", cfg.Agent.Name, "skills", len(cfg.Skills), "card", len(h.Card().Skills), "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func configFromEnv() (host.Config, string, error) {
	instructions := os.Getenv("CORTEX_INSTRUCTIONS")
	if path := os.Getenv("CORTEX_INSTRUCTIONS_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return host.Config{}, "", fmt.Errorf("CORTEX_INSTRUCTIONS_FILE: %w", err)
		}
		instructions = string(b)
	}
	cfg := host.Config{
		Agent: host.Agent{
			Name:         os.Getenv("CORTEX_AGENT_NAME"),
			Description:  os.Getenv("CORTEX_AGENT_DESCRIPTION"),
			Instructions: instructions,
		},
		Provider: agent.Provider{
			BaseURL: os.Getenv("CORTEX_BASE_URL"),
			APIKey:  os.Getenv("CORTEX_API_KEY"),
			Model:   envOr("CORTEX_MODEL", "lalter"),
		},
		Token:         os.Getenv("CORTEX_TOKEN"),
		PublicURL:     os.Getenv("CORTEX_PUBLIC_URL"),
		MaxSteps:      envInt("CORTEX_MAX_STEPS"),
		ContextWindow: envInt("CORTEX_CONTEXT_WINDOW"),
		Version:       version,
	}
	web, err := webToolsFromEnv()
	if err != nil {
		return cfg, "", err
	}
	cfg.Tools = web
	verify, err := callersFromEnv()
	if err != nil {
		return cfg, "", err
	}
	cfg.Verify = verify
	if cfg.Token == "" && cfg.Verify == nil {
		return cfg, "", fmt.Errorf("CORTEX_TOKEN or CORTEX_ISSUER_URL is required: every caller of /a2a proves who it is")
	}
	cfg.Provider.HTTPClient = &http.Client{Transport: lentToken{}}
	if path := os.Getenv("CORTEX_MCP_CONFIG"); path != "" {
		m, err := mcp.ReadConfigFile(path)
		if err != nil {
			return cfg, "", err
		}
		cfg.MCP = m
	}
	servers, err := mcpServersFromEnv(os.Getenv("CORTEX_MCP_SERVERS"))
	if err != nil {
		return cfg, "", err
	}
	cfg.MCP.Servers = append(cfg.MCP.Servers, servers...)
	cfg.Recall = recallFromEnv()
	return cfg, envOr("CORTEX_ADDR", ":7400"), nil
}

// mcpServersFromEnv reads remote MCP servers written "name=url,name=url",
// for a runtime that sets an environment but mounts no file.
func mcpServersFromEnv(raw string) ([]mcp.ServerConfig, error) {
	var servers []mcp.ServerConfig
	for _, entry := range strings.Split(raw, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		name, url, ok := strings.Cut(entry, "=")
		name, url = strings.TrimSpace(name), strings.TrimSpace(url)
		if !ok || name == "" || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
			return nil, fmt.Errorf("CORTEX_MCP_SERVERS: %q is not name=http(s)://url", strings.TrimSpace(entry))
		}
		servers = append(servers, mcp.ServerConfig{Name: name, URL: url})
	}
	return servers, nil
}

// recallFromEnv keeps the agent's memory in lalter when CORTEX_RECALL_URL
// names lalter's API; the key defaults to the model key, one app key with
// both the llm and recall scopes. A turn its caller lent a token to calls
// under that token instead.
func recallFromEnv() recall.Store {
	base := strings.TrimSpace(os.Getenv("CORTEX_RECALL_URL"))
	if base == "" {
		return nil
	}
	return lalterrecall.New(lalterrecall.Config{
		BaseURL: base,
		AppKey:  envOr("CORTEX_RECALL_KEY", os.Getenv("CORTEX_API_KEY")),
		Client:  &http.Client{Timeout: 15 * time.Second, Transport: lentToken{}},
	})
}

// lentToken presents the token the turn's caller lent, when it lent one, in
// place of the configured key.
type lentToken struct{}

func (lentToken) RoundTrip(req *http.Request) (*http.Response, error) {
	if token := host.TurnToken(req.Context()); token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// callersFromEnv accepts, as callers of /a2a, the tokens CORTEX_ISSUER_URL
// signed for CORTEX_AUDIENCE, checked offline against its keys at
// CORTEX_JWKS_URL (by default the issuer's /.well-known/jwks.json). When
// CORTEX_CALLERS is set, the token must also name one of its clients.
func callersFromEnv() (func(context.Context, string) error, error) {
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv("CORTEX_ISSUER_URL")), "/")
	if issuer == "" {
		return nil, nil
	}
	audience := strings.TrimSpace(os.Getenv("CORTEX_AUDIENCE"))
	if audience == "" {
		return nil, fmt.Errorf("CORTEX_ISSUER_URL needs CORTEX_AUDIENCE")
	}
	verifier, err := svcauth.New([]svcauth.Issuer{{
		URL:       issuer,
		JWKSURL:   envOr("CORTEX_JWKS_URL", issuer+"/.well-known/jwks.json"),
		Audiences: []string{audience},
	}})
	if err != nil {
		return nil, fmt.Errorf("CORTEX_ISSUER_URL: %w", err)
	}
	callers := splitList(os.Getenv("CORTEX_CALLERS"))
	return func(ctx context.Context, bearer string) error {
		claims, err := verifier.Verify(ctx, bearer)
		if err != nil {
			return err
		}
		if len(callers) == 0 {
			return nil
		}
		for _, c := range callers {
			if claims.ClientID == c {
				return nil
			}
		}
		return fmt.Errorf("client %q may not call this agent", claims.ClientID)
	}, nil
}

func splitList(raw string) []string {
	var out []string
	for _, v := range strings.Split(raw, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}
