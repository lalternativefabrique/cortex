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
	"sync"

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
	// SkillKey names the declared skill a message runs: the host applies
	// its instructions and output schema, and checks the message text, the
	// skill's input as JSON, against its input schema.
	SkillKey = "skill"
	// ModelKey asks for a model for this run in place of the agent's own;
	// the endpoint the agent calls decides whether it serves it.
	ModelKey = "model"
	// ReasoningEffortKey asks how much the model thinks before answering on
	// this run: high, medium, low or none.
	ReasoningEffortKey = "reasoningEffort"
	// RecallKey false runs the turn without memory: nothing recalled, nothing
	// written.
	RecallKey = "recall"
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
	// Skills are the actions the app declared for this agent (skills.json):
	// listed on the Agent Card and at /skills, run by name.
	Skills []Skill
	// AppInstructions are the app's own instructions (skills.json
	// "instructions"), applied to every run after Agent.Instructions.
	AppInstructions string
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
	// Turns decides where and when turns run. Nil runs them in this process,
	// DefaultConcurrency at once.
	Turns Turns
	// History keeps conversations between turns. Nil keeps them in memory,
	// which only one instance can share.
	History History
	// OnSkillsRefreshed is called after POST /skills/refresh reloaded this
	// instance's skills, so the other instances can reload theirs.
	OnSkillsRefreshed func(ctx context.Context)
}

type Host struct {
	cfg             Config
	skillsMu        sync.RWMutex
	skillSource     func(context.Context) (Catalog, error)
	appInstructions string
	skillList       []Skill
	skills          map[string]declaredSkill
	servers         *servers
	turns           Turns
	history         History
	handler         http.Handler
}

func New(ctx context.Context, cfg Config) (*Host, error) {
	if strings.TrimSpace(cfg.Agent.Name) == "" {
		return nil, fmt.Errorf("host: the agent needs a name")
	}
	if strings.TrimSpace(cfg.Provider.BaseURL) == "" {
		return nil, fmt.Errorf("host: a model endpoint is required")
	}
	skills, err := compileSkills(cfg.Skills)
	if err != nil {
		return nil, err
	}
	h := &Host{cfg: cfg, skillList: cfg.Skills, skills: skills, appInstructions: cfg.AppInstructions, servers: startServers(ctx, cfg.MCP), turns: cfg.Turns, history: cfg.History}
	if h.history == nil {
		h.history = NewMemoryHistory(maxConversations)
	}
	if h.turns == nil {
		h.turns = NewLocalTurns(DefaultConcurrency, DefaultQueueWait)
	}
	if local, ok := h.turns.(*LocalTurns); ok {
		local.bind(h.run)
	}
	h.handler = a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(&executor{host: h}))
	return h, nil
}

// Serve takes turns until ctx ends. A host whose Turns run in this process
// answers without it.
func (h *Host) Serve(ctx context.Context) error {
	return h.turns.Serve(ctx, h.run)
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
	mux.HandleFunc("GET /skills", h.serveSkills)
	mux.HandleFunc("GET /context", h.serveContext)
	mux.Handle("POST /skills/refresh", h.authorized(http.HandlerFunc(h.refreshSkills)))
	mux.Handle("POST /a2a", h.authorized(watchCaller(h.handler)))
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
	list, declared := h.declaredSkills()
	skills := make([]a2a.AgentSkill, 0, len(list)+len(tools)+1)
	for _, s := range list {
		skills = append(skills, declared[s.ID].skill())
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
		Capabilities:       a2a.AgentCapabilities{Streaming: true, Extensions: []a2a.AgentExtension{cortexExtension()}},
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

// ExtensionURI names what cortex adds to A2A. A standard client can call the
// agent without it; one that knows it can run a declared skill, ask for an
// output format and lend the turn an identity.
const ExtensionURI = "https://lalter.fr/a2a/ext/cortex/v1"

func cortexExtension() a2a.AgentExtension {
	return a2a.AgentExtension{
		URI:         ExtensionURI,
		Description: "Message metadata cortex reads, the artifacts it adds, and where its skills are declared in full.",
		Params: map[string]any{
			"metadata": map[string]string{
				SkillKey:           "the declared skill the message runs; the message text is then its input, as JSON",
				ModelKey:           "a model for this run in place of the agent's own; the model endpoint decides whether it serves it",
				OutputSchemaKey:    "a JSON Schema the answer must match; the answer then comes as one data part of the answer artifact",
				SubjectKey:         "the person the run acts for, its memory scope",
				TurnContextKey:     "text added to the agent's instructions for this run only",
				MCPHeadersKey:      "headers every MCP call of the run sends, never shown to the model",
				TurnTokenKey:       "the Bearer the run's model and memory calls present",
				ReasoningEffortKey: "how much the model thinks on this run: high, medium, low or none",
				RecallKey:          "false runs without memory: nothing recalled, nothing written",
			},
			"artifacts": map[string]string{
				"answer":    "the answer: text chunks, or one data part under an output schema",
				"tool-call": "one per tool call: the tool, its arguments, its result or error",
				"step":      "one per model call: its reasoning, the tools it asked for, its tokens and duration",
			},
			"context": "/context serves the agent's own instructions, default model, tools and MCP servers",
			"skills":  "/skills serves the app's catalog: its instructions (applied to every run) and every declared skill in full: instructions, input and output JSON Schemas, examples; POST /skills/refresh (same Bearer as /a2a) loads them again from the app",
		},
	}
}

// AgentContext is what an agent runs with besides its skills: what a
// console shows so a person sees what the agent is told before any skill.
type AgentContext struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Instructions    string   `json:"instructions"`
	AppInstructions string   `json:"app_instructions"`
	Model           string   `json:"model"`
	Tools           []string `json:"tools"`
	MCPServers      []string `json:"mcp_servers"`
	Memory          bool     `json:"memory"`
	Version         string   `json:"version"`
}

// Context reports the agent's own instructions, model, tools and servers.
func (h *Host) Context() AgentContext {
	c := AgentContext{
		Name:            h.cfg.Agent.Name,
		Description:     h.cfg.Agent.Description,
		Instructions:    h.cfg.Agent.Instructions,
		AppInstructions: h.appInstructionsNow(),
		Model:           h.cfg.Provider.Model,
		Tools:           []string{},
		MCPServers:      []string{},
		Memory:          h.cfg.Recall != nil,
		Version:         h.cfg.Version,
	}
	for _, t := range append(append([]agent.Tool(nil), h.cfg.Tools...), h.servers.offered()...) {
		c.Tools = append(c.Tools, t.Name())
	}
	for _, s := range h.cfg.MCP.Servers {
		c.MCPServers = append(c.MCPServers, s.Name)
	}
	return c
}

func (h *Host) serveContext(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.Context())
}
