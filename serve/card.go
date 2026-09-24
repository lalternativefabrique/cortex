package serve

import (
	"net/http"
	"slices"
	"strings"

	"github.com/a2aproject/a2a-go/a2a"
)

// Version is what the Agent Card reports. A build stamps it through
// -ldflags; an unstamped build says so rather than claiming a release.
var Version = "dev"

const (
	cardPath   = "/.well-known/agent.json"
	bearerName = "bearer"
)

// Card builds this instance's Agent Card. It is derived from the tools New
// actually assembled, never from Config: a card that lists what the
// deployment could not wire would send clients to skills that answer errors.
func (s *Server) Card() a2a.AgentCard {
	return a2a.AgentCard{
		Name:        "cortex",
		Description: "An agent that works in a workspace: reads and writes its files, runs commands in it, and answers about what it finds there.",
		URL:         strings.TrimSuffix(s.cfg.PublicURL, "/") + a2aPath,
		Version:     Version,
		// The card's url must be the endpoint its transport is served on, and
		// A2A arrives as JSON-RPC on /a2a. The legacy /turn surface speaks its
		// own JSON and is not what a peer is pointed at.
		PreferredTransport: a2a.TransportProtocolJSONRPC,
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: false,
			// Every turn is written to a session store as it is produced, and
			// GET /sessions replays them.
			StateTransitionHistory: true,
		},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			bearerName: a2a.HTTPAuthSecurityScheme{
				Scheme:      "Bearer",
				Description: "The token the server was started with.",
			},
		},
		Security:           []a2a.SecurityRequirements{{bearerName: a2a.SecuritySchemeScopes{}}},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             s.cardSkills(),
	}
}

// cardSkills advertises one skill per tool the instance ended up with, plus
// the SKILL.md files found in the workspace. A tool left out for want of a
// backend is absent here too.
func (s *Server) cardSkills() []a2a.AgentSkill {
	out := make([]a2a.AgentSkill, 0, len(s.tools)+len(s.skills))
	for _, t := range s.tools {
		out = append(out, a2a.AgentSkill{
			ID:          t.Name(),
			Name:        t.Name(),
			Description: firstLine(t.Description()),
			Tags:        []string{"tool"},
		})
	}
	for _, sk := range s.skills {
		// A card entry with no description tells a client a name and nothing
		// it could decide on, so an undocumented skill is left out rather
		// than advertised blank.
		if strings.TrimSpace(sk.Description) == "" {
			continue
		}
		out = append(out, a2a.AgentSkill{
			ID:          sk.Name,
			Name:        sk.Name,
			Description: firstLine(sk.Description),
			Tags:        []string{"skill"},
		})
	}
	slices.SortFunc(out, func(a, b a2a.AgentSkill) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// firstLine keeps a card readable: a tool's description is written for the
// model and runs to paragraphs, where a card entry is a one-line label.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// handleCard serves the Agent Card. It sits outside the token, like /health:
// a card is what a client reads to learn how to authenticate, so requiring
// authentication to read it would be circular. It names the scheme and the
// skills, never the workspace path nor the token itself.
func (s *Server) handleCard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Card())
}
