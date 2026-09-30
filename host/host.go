// Package host runs one declared agent behind A2A, for a container: no
// workspace, no shell, only the tools its MCP servers offer and the memory
// its host hands it (lalter ADR 0013). Callers speak JSON-RPC, and
// message/stream streams the answer as it is written.
package host

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2asrv"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/mcp"
	"github.com/lalternative/packages/go/cortex/recall"
)

// Metadata keys a caller sets on its A2A message. Only a caller holding the
// token reaches the agent, and it names these for its own users.
const (
	// SubjectKey names the person the turn acts for: its memory scope.
	SubjectKey = "subject"
	// MCPHeadersKey carries headers every MCP call of the turn sends, such as
	// the person's grant, beside the tool call and never to the model.
	MCPHeadersKey = "mcpHeaders"
	// TurnContextKey is text added to the agent's instructions for this turn
	// only: what the caller knows of the moment (the scope the person chose,
	// their time zone). It is not remembered.
	TurnContextKey = "turnContext"
	// TurnTokenKey carries the Bearer the turn's model and memory calls
	// present in place of the configured key: the caller lends the agent the
	// identity it acts under, for this turn only. See TurnToken.
	TurnTokenKey = "turnToken"
	// OutputSchemaKey carries a JSON Schema the turn's answer must match.
	// The turn then ends with the model calling respond, the answer is
	// checked against the schema, and it is returned as one data part of the
	// "answer" artifact instead of text.
	OutputSchemaKey = "outputSchema"
	// SkillKey names the declared task a turn runs: the host applies its
	// instructions and output schema, and checks the message text, the
	// task's input as JSON, against its input schema.
	SkillKey = "skill"
)

// Agent is what the container declares itself to be.
type Agent struct {
	Name         string
	Description  string
	Instructions string
}

type Config struct {
	Agent    Agent
	Provider agent.Provider
	MCP      mcp.Config
	// Tools are offered on every turn beside the MCP servers' tools: what
	// the host itself can do, such as reading the web.
	Tools []agent.Tool
	// Tasks are the actions the app declared for this agent (tasks.json):
	// listed on the Agent Card and at /tasks, run by name.
	Tasks []Task
	// Recall nil keeps no memory; recall_memory is offered only with it.
	Recall recall.Store
	// Token is what every caller of /a2a presents as Bearer. Empty refuses
	// everyone, unless Verify is set.
	Token string
	// Verify, when set, decides a caller of /a2a from the Bearer it presents,
	// in place of Token: an identity provider's token, for instance.
	Verify func(ctx context.Context, bearer string) error
	// PublicURL is where callers reach this container, for its Agent Card.
	PublicURL     string
	MaxSteps      int
	ContextWindow int
	// Version is what the Agent Card reports.
	Version string
}

type Host struct {
	cfg           Config
	tasks         map[string]declaredTask
	servers       *servers
	conversations *conversations
	handler       http.Handler
}

func New(ctx context.Context, cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Agent.Name) == "" {
		return nil, fmt.Errorf("host: the agent needs a name")
	}
	if strings.TrimSpace(cfg.Provider.BaseURL) == "" {
		return nil, fmt.Errorf("host: a model endpoint is required")
	}
	tasks, err := compileTasks(cfg.Tasks)
	if err != nil {
		return nil, err
	}
	h := &Host{cfg: cfg, tasks: tasks, servers: startServers(ctx, cfg.MCP), conversations: newConversations(maxConversations)}
	h.handler = a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(&executor{host: h}))
	return h, nil
}

// Close stops the MCP servers the host started.
func (h *Host) Close() {
	h.servers.close()
}

// Handler serves the Agent Card, /a2a and /health.
func (h *Host) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /.well-known/agent.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(h.Card())
	})
	mux.HandleFunc("GET /tasks", h.serveTasks)
	mux.Handle("POST /a2a", h.authorized(h.handler))
	return mux
}

func (h *Host) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !h.accepts(r.Context(), got) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cortex"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Host) accepts(ctx context.Context, bearer string) bool {
	if h.cfg.Verify != nil {
		return h.cfg.Verify(ctx, bearer) == nil
	}
	return h.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(h.cfg.Token)) == 1
}

// Card describes the agent: what it is, where to reach it, and the tools it
// can call, its own and those its reachable MCP servers offer.
func (h *Host) Card() a2a.AgentCard {
	tools := append(append([]agent.Tool(nil), h.cfg.Tools...), h.servers.offered()...)
	skills := make([]a2a.AgentSkill, 0, len(h.cfg.Tasks)+len(tools)+1)
	for _, t := range h.cfg.Tasks {
		skills = append(skills, h.tasks[t.ID].skill())
	}
	for _, t := range tools {
		skills = append(skills, a2a.AgentSkill{ID: t.Name(), Name: t.Name(), Description: firstLine(t.Description()), Tags: []string{"tool"}})
	}
	if h.cfg.Recall != nil {
		skills = append(skills, a2a.AgentSkill{ID: "recall_memory", Name: "recall_memory", Description: "Remembers past conversations with the person.", Tags: []string{"memory"}})
	}
	version := h.cfg.Version
	if version == "" {
		version = "dev"
	}
	return a2a.AgentCard{
		Name:               h.cfg.Agent.Name,
		Description:        h.cfg.Agent.Description,
		URL:                strings.TrimSuffix(h.cfg.PublicURL, "/") + "/a2a",
		Version:            version,
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		Capabilities:       a2a.AgentCapabilities{Streaming: true},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer", Description: "The token this container was started with."},
		},
		Security:           []a2a.SecurityRequirements{{"bearer": a2a.SecuritySchemeScopes{}}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain", "application/json"},
		Skills:             skills,
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
