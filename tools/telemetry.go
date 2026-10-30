package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

// TelemetryConfig points a task at the evidence its repair is about.
//
// The agent is fixing a production failure, and what proves the failure is
// not in the repository: it is the logs, the spans and the metrics the
// platform collected. Reading them turns a diagnosis quoted in the prompt
// into something the agent can check for itself.
type TelemetryConfig struct {
	// BaseURL is the API to ask, e.g. https://api.skalpai.dev. Empty
	// disables the tools entirely.
	BaseURL string
	// Token authenticates the read. It is scoped by the caller to this one
	// project's telemetry and expires with the run, so it is worth no more
	// than what these tools do with it.
	Token string
	// ProjectID is the only project these tools can read.
	ProjectID string
	// Timeout bounds one query. Zero means defaultTelemetryTimeout.
	Timeout time.Duration
}

const (
	defaultTelemetryTimeout = 30 * time.Second
	telemetryMaxBytes       = 256 << 10
	defaultTelemetryLimit   = 50
	maxTelemetryLimit       = 200
)

// NewTelemetry returns the telemetry tools, or nothing when no access was
// granted — a task that cannot read telemetry still repairs code.
func NewTelemetry(cfg TelemetryConfig) []agent.Tool {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.ProjectID) == "" {
		return nil
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return []agent.Tool{
		&telemetryTool{
			cfg:      cfg,
			name:     "search_logs",
			endpoint: "logs",
			summary:  "Search the logs this project sent to Skalpai. Use it to confirm a failure actually happens, how often, and with what message — the repository cannot tell you that.",
		},
		&telemetryTool{
			cfg:      cfg,
			name:     "search_spans",
			endpoint: "traces",
			summary:  "Search this project's traces. Use it to see which call path a failing request took and where the time or the error came from.",
		},
		&telemetryTool{
			cfg:      cfg,
			name:     "get_metrics",
			endpoint: "metrics",
			summary:  "Read this project's metrics. Use it to check whether a symptom is a spike, a trend, or nothing at all.",
		},
	}
}

type telemetryArgs struct {
	Query    string `json:"query,omitempty" jsonschema:"description=Free-text filter over messages and attributes. Omit to see everything in the window."`
	Severity string `json:"severity,omitempty" jsonschema:"description=Severity to keep, e.g. ERROR. Logs only."`
	From     string `json:"from,omitempty" jsonschema:"description=Start of the window as an RFC 3339 instant. Omit for the API's own default."`
	To       string `json:"to,omitempty" jsonschema:"description=End of the window as an RFC 3339 instant."`
	Limit    int    `json:"limit,omitempty" jsonschema:"description=Maximum rows to return. Defaults to 50."`
}

type telemetryTool struct {
	cfg      TelemetryConfig
	name     string
	endpoint string
	summary  string
}

func (t *telemetryTool) Name() string { return t.name }

func (t *telemetryTool) Description() string {
	return t.summary + " It reads only this task's project, and only what its owner could see."
}

func (t *telemetryTool) InputSchema() any { return telemetryArgs{} }

func (t *telemetryTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args telemetryArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return failure("could not parse arguments: %v", err)
		}
	}

	limit := args.Limit
	if limit <= 0 {
		limit = defaultTelemetryLimit
	}
	if limit > maxTelemetryLimit {
		limit = maxTelemetryLimit
	}

	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	if args.Query != "" {
		q.Set("q", args.Query)
	}
	if args.Severity != "" {
		q.Set("severity", args.Severity)
	}
	if args.From != "" {
		q.Set("from", args.From)
	}
	if args.To != "" {
		q.Set("to", args.To)
	}

	endpoint := fmt.Sprintf("%s/api/projects/%s/%s?%s",
		t.cfg.BaseURL, url.PathEscape(t.cfg.ProjectID), t.endpoint, q.Encode())

	timeout := t.cfg.Timeout
	if timeout == 0 {
		timeout = defaultTelemetryTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return failure("%v", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.cfg.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return failure("%s: %v", t.name, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, telemetryMaxBytes))
	if err != nil {
		return failure("%s: read response: %v", t.name, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return failure("%s: this task is not allowed to read that (%d)", t.name, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return failure("%s: the API returned %d: %s", t.name, resp.StatusCode, clip(string(body), 300))
	}
	return agent.ToolResult{Content: string(body)}, nil
}
