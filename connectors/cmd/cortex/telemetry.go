package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"strings"

	skalpai "github.com/lalternative/packages/skalpai/sdk-go"

	"github.com/lalternative/packages/go/cortex/host"
)

// telemetry sends the container's logs, traces and metrics to skalpai when
// SKALPAI_ENDPOINT and SKALPAI_API_KEY are set, the service named after the
// agent. Without them the container logs to stdout only, as a self-hosted
// one does.
func telemetry(ctx context.Context, agentName string) (func(context.Context) error, error) {
	endpoint, key := os.Getenv("SKALPAI_ENDPOINT"), os.Getenv("SKALPAI_API_KEY")
	stdout := slog.NewJSONHandler(os.Stdout, nil)
	if endpoint == "" || key == "" {
		slog.SetDefault(slog.New(turnHandler{stdout}))
		return func(context.Context) error { return nil }, nil
	}
	service := envOr("SKALPAI_SERVICE", agentName)
	shutdown, err := skalpai.Init(ctx, skalpai.Config{
		Endpoint:       endpoint,
		APIKey:         key,
		ServiceName:    service,
		ServiceVersion: version,
		Environment:    envOr("SKALPAI_ENVIRONMENT", "production"),
	})
	if err != nil {
		slog.SetDefault(slog.New(turnHandler{stdout}))
		return func(context.Context) error { return nil }, err
	}
	slog.SetDefault(slog.New(turnHandler{skalpai.Fanout(stdout, skalpai.NewSlogHandler(service))}))
	return shutdown, nil
}

// turnHandler names, on every record logged under a turn, the turn its
// caller lent: the id of the token, which lalter also logs on its side, so a
// turn's story in the container and in lalter meet under one id.
type turnHandler struct {
	slog.Handler
}

func (h turnHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := turnID(host.TurnToken(ctx)); id != "" {
		r.AddAttrs(slog.String("turn_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h turnHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return turnHandler{h.Handler.WithAttrs(attrs)}
}

func (h turnHandler) WithGroup(name string) slog.Handler {
	return turnHandler{h.Handler.WithGroup(name)}
}

// turnID reads the id of a lent token without checking it: the door already
// did, and the id only names the turn in the logs.
func turnID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		ID string `json:"jti"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.ID
}
