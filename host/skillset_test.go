package host

import (
	"strings"
	"testing"

	"github.com/lalternativefabrique/cortex/skills"
)

func TestATurnOffersTheSkillsByNameAndOpensThemOnDemand(t *testing.T) {
	provider, bodies := scriptedModel(t, scriptedReply{text: "Salut."})
	set := skills.Set{"seo-audit": {Name: "seo-audit", Description: "Check a site's ranking.", Body: "Search each keyword."}}
	srv := start(t, Config{Agent: Agent{Name: "ego"}, Provider: provider, SkillSet: set, Token: "t"})

	readStream(t, rpc(t, srv.URL, "t", "message/stream", message("bonjour", "ctx-sk", nil)))

	body := (*bodies)[0]
	if !strings.Contains(body, skills.ToolName) || !strings.Contains(body, "seo-audit: Check a site's ranking.") {
		t.Fatalf("the model is not offered the skills:\n%s", body)
	}
	if strings.Contains(body, "Search each keyword.") {
		t.Fatalf("a skill's body is read on demand, never offered up front:\n%s", body)
	}
}
