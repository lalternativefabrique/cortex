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
)

var version = "dev"

func main() {
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
	slog.Info("cortex listening", "addr", addr, "agent", cfg.Agent.Name, "tools", len(h.Card().Skills), "version", version)
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
	if cfg.Token == "" {
		return cfg, "", fmt.Errorf("CORTEX_TOKEN is required: every caller of /a2a presents it")
	}
	if path := os.Getenv("CORTEX_MCP_CONFIG"); path != "" {
		m, err := mcp.ReadConfigFile(path)
		if err != nil {
			return cfg, "", err
		}
		cfg.MCP = m
	}
	cfg.Recall = recallFromEnv()
	return cfg, envOr("CORTEX_ADDR", ":7400"), nil
}

// recallFromEnv keeps the agent's memory in lalter when CORTEX_RECALL_URL
// names lalter's API; the key defaults to the model key, one app key with
// both the llm and recall scopes.
func recallFromEnv() recall.Store {
	base := strings.TrimSpace(os.Getenv("CORTEX_RECALL_URL"))
	if base == "" {
		return nil
	}
	return lalterrecall.New(lalterrecall.Config{BaseURL: base, AppKey: envOr("CORTEX_RECALL_KEY", os.Getenv("CORTEX_API_KEY"))})
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
