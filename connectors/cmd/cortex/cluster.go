package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/lalternative/packages/go/cortex/connectors/cluster"
	"github.com/lalternative/packages/go/cortex/host"
)

const defaultHistoryTTL = 7 * 24 * time.Hour

// clustered is how turns are spread: over every instance sharing
// CORTEX_NATS_URL, or in this process alone without it. start runs once the
// host exists; stop releases the connection.
type clustered struct {
	start func(ctx context.Context, h *host.Host) error
	stop  func()
}

func clusterFromEnv(ctx context.Context, cfg *host.Config) (clustered, error) {
	concurrency := envInt("CORTEX_CONCURRENCY")
	wait, err := envDuration("CORTEX_QUEUE_WAIT")
	if err != nil {
		return clustered{}, err
	}
	url := strings.TrimSpace(os.Getenv("CORTEX_NATS_URL"))
	if url == "" {
		cfg.Turns = host.NewLocalTurns(concurrency, wait)
		slog.Info("cortex: turns run in this instance only", "concurrency", max(concurrency, 0))
		return clustered{start: func(context.Context, *host.Host) error { return nil }, stop: func() {}}, nil
	}

	opts := []nats.Option{nats.Name("cortex-" + cfg.Agent.Name), nats.MaxReconnects(-1), nats.ReconnectWait(2 * time.Second), nats.RetryOnFailedConnect(true)}
	if user := os.Getenv("CORTEX_NATS_USER"); user != "" {
		opts = append(opts, nats.UserInfo(user, os.Getenv("CORTEX_NATS_PASSWORD")))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return clustered{}, fmt.Errorf("CORTEX_NATS_URL: %w", err)
	}
	turns, err := cluster.NewTurns(ctx, nc, cluster.Config{Agent: cfg.Agent.Name, Concurrency: concurrency, QueueWait: wait})
	if err != nil {
		nc.Close()
		return clustered{}, err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return clustered{}, err
	}
	ttl, err := envDuration("CORTEX_HISTORY_TTL")
	if err != nil {
		nc.Close()
		return clustered{}, err
	}
	if ttl <= 0 {
		ttl = defaultHistoryTTL
	}
	history, err := cluster.NewHistory(ctx, js, cfg.Agent.Name, ttl)
	if err != nil {
		nc.Close()
		return clustered{}, err
	}
	cfg.Turns, cfg.History = turns, history

	instance, _ := os.Hostname()
	served := make(chan struct{})
	var refresh *cluster.SkillsRefresh
	stopRefresh := func() {}
	cfg.OnSkillsRefreshed = func(ctx context.Context) {
		if refresh != nil {
			refresh.Announce(ctx)
		}
	}
	slog.Info("cortex: turns spread over NATS", "url", url, "concurrency", max(concurrency, 0), "instance", instance)
	return clustered{
		start: func(ctx context.Context, h *host.Host) error {
			r, stop, err := cluster.NewSkillsRefresh(nc, cfg.Agent.Name, instance, func(ctx context.Context) {
				if _, err := h.RefreshSkills(ctx); err != nil {
					slog.Warn("cortex: skills not refreshed on announce", "error", err)
				}
			})
			if err != nil {
				return err
			}
			refresh, stopRefresh = r, stop
			go func() {
				defer close(served)
				if err := h.Serve(ctx); err != nil {
					slog.Error("cortex: turns no longer served", "error", err)
				}
			}()
			return nil
		},
		stop: func() {
			select {
			case <-served:
			case <-time.After(cluster.DefaultDrain + 5*time.Second):
				slog.Warn("cortex: turns still running at exit")
			}
			stopRefresh()
			_ = nc.Drain()
		},
	}, nil
}

func envDuration(name string) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}
