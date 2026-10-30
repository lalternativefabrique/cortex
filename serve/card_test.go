package serve

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/lalternative/packages/go/cortex/agent"
)

// isolateSkills points the skill search and the session store at empty
// directories, so a test reads the tools this Config wired rather than
// whatever the machine running it has installed, and writes its transcripts
// away from the user's own.
func isolateSkills(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
}

func cardOf(t *testing.T, cfg Config) a2a.AgentCard {
	t.Helper()
	isolateSkills(t)
	cfg.Root = t.TempDir()
	cfg.Provider = agent.Provider{Model: "test-model"}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv.Card()
}

func skillIDs(card a2a.AgentCard) []string {
	out := make([]string, 0, len(card.Skills))
	for _, s := range card.Skills {
		out = append(out, s.ID)
	}
	return out
}

func TestCardListsOnlyWiredTools(t *testing.T) {
	card := cardOf(t, Config{})
	ids := skillIDs(card)

	for _, want := range []string{"read", "grep", "glob", "edit", "write", "bash"} {
		if !slices.Contains(ids, want) {
			t.Errorf("base tool %q missing from card, got %v", want, ids)
		}
	}
	// No vision model was configured, so the deployment cannot describe an
	// image and the card must not send a client asking for it.
	if slices.Contains(ids, "describe_image") {
		t.Errorf("describe_image advertised without a vision model, got %v", ids)
	}
	// No search backend either.
	if slices.Contains(ids, "web_search") {
		t.Errorf("web_search advertised without a backend, got %v", ids)
	}
}

func TestCardDeclaresBearerSecurity(t *testing.T) {
	card := cardOf(t, Config{})

	scheme, ok := card.SecuritySchemes[bearerName]
	if !ok {
		t.Fatalf("no %q security scheme, got %v", bearerName, card.SecuritySchemes)
	}
	http, ok := scheme.(a2a.HTTPAuthSecurityScheme)
	if !ok {
		t.Fatalf("scheme is %T, want an HTTP auth scheme", scheme)
	}
	if http.Scheme != "Bearer" {
		t.Errorf("scheme is %q, want Bearer", http.Scheme)
	}
	if len(card.Security) == 0 {
		t.Error("card declares a scheme but does not require it")
	}
}

// The card's url is where a peer sends A2A calls, which is the protocol
// endpoint and not the host it happens to sit on.
func TestCardAdvertisesTheA2AEndpoint(t *testing.T) {
	card := cardOf(t, Config{PublicURL: "https://cortex.example.test"})

	if want := "https://cortex.example.test" + a2aPath; card.URL != want {
		t.Errorf("card url is %q, want %q", card.URL, want)
	}
	if card.PreferredTransport != a2a.TransportProtocolJSONRPC {
		t.Errorf("transport is %q, want JSONRPC", card.PreferredTransport)
	}
}

func TestCardURLToleratesTrailingSlash(t *testing.T) {
	card := cardOf(t, Config{PublicURL: "https://cortex.example.test/"})

	if want := "https://cortex.example.test" + a2aPath; card.URL != want {
		t.Errorf("card url is %q, want %q", card.URL, want)
	}
}

// The card is what a client reads to learn how to authenticate, so it has to
// be readable without authenticating first.
func TestCardServedWithoutToken(t *testing.T) {
	isolateSkills(t)
	cfg := Config{Root: t.TempDir(), Provider: agent.Provider{Model: "test-model"}, Token: "secret"}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go srv.Serve(ln)

	res, err := http.Get("http://" + ln.Addr().String() + cardPath)
	if err != nil {
		t.Fatalf("GET %s: %v", cardPath, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status is %d, want 200", res.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if card.Name == "" || len(card.Skills) == 0 {
		t.Errorf("card is empty: %+v", card)
	}
}

func TestCardSkillDescriptionIsOneLine(t *testing.T) {
	card := cardOf(t, Config{})
	for _, s := range card.Skills {
		if s.Description == "" {
			t.Errorf("skill %q is advertised with no description", s.ID)
		}
		for _, r := range s.Description {
			if r == '\n' {
				t.Errorf("skill %q description spans lines: %q", s.ID, s.Description)
				break
			}
		}
	}
}
