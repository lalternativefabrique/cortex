package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
	"github.com/lalternativefabrique/cortex/host"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	token := os.Getenv("CORTEX_TOKEN")
	if token == "" {
		log.Fatal("CORTEX_TOKEN is required")
	}

	h, err := host.New(ctx, host.Config{
		Agent: host.Agent{Name: "hello", Instructions: "Answer briefly."},
		Provider: agent.Provider{
			BaseURL: os.Getenv("CORTEX_BASE_URL"),
			APIKey:  os.Getenv("CORTEX_API_KEY"),
			Model:   os.Getenv("CORTEX_MODEL"),
			Timeout: 2 * time.Minute,
		},
		Token:          token,
		PublicURL:      os.Getenv("CORTEX_PUBLIC_URL"),
		MaxSteps:       10,
		MaxInputTokens: 200_000,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	srv := &http.Server{Addr: ":7400", Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
