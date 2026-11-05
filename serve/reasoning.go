package serve

import (
	"net/http"

	"github.com/lalternativefabrique/cortex/agent"
)

// ReasoningLevels is what a composer offers: the efforts a turn may ask for,
// and the one a turn that asks nothing gets.
type ReasoningLevels struct {
	Options []string `json:"options"`
	Default string   `json:"default,omitempty"`
}

func (s *Server) handleReasoning(w http.ResponseWriter, _ *http.Request) {
	def := s.cfg.Provider.ReasoningEffort
	if def == "" {
		def = agent.DefaultReasoningEffort(s.cfg.Provider.Model)
	}
	writeJSON(w, http.StatusOK, ReasoningLevels{Options: agent.ReasoningEfforts, Default: def})
}
