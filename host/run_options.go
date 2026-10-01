package host

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lalternative/packages/go/cortex/agent"
)

func recallWanted(meta map[string]any) bool {
	wanted, ok := meta[RecallKey].(bool)
	return !ok || wanted
}

// reasoningEffort is the run's own ask, else the agent's, else what the model
// is known to need.
func reasoningEffort(meta map[string]any, provider agent.Provider) (string, error) {
	if asked, _ := meta[ReasoningEffortKey].(string); strings.TrimSpace(asked) != "" {
		asked = strings.TrimSpace(asked)
		if !agent.ValidReasoningEffort(asked) {
			return "", fmt.Errorf("%s %q is not one of %s", ReasoningEffortKey, asked, strings.Join(agent.ReasoningEfforts, ", "))
		}
		return asked, nil
	}
	if provider.ReasoningEffort != "" {
		return provider.ReasoningEffort, nil
	}
	return agent.DefaultReasoningEffort(provider.Model), nil
}

// turnTokenExpired reads a lent JWT's exp without checking its signature:
// the endpoint it is presented to checks that. It only spares a turn that
// queued past its identity's life a run bound to fail on its first call.
func turnTokenExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return !now.Before(time.Unix(claims.Exp, 0))
}
