package skill

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lalternative/packages/go/cortex/agent"
	"github.com/lalternative/packages/go/cortex/client"
	"github.com/lalternative/packages/go/cortex/host"
)

type styleInput struct {
	Brand string `json:"brand" jsonschema:"description=What the brand's site says."`
}

type styleAnswer struct {
	Category string `json:"category"`
	Energy   int    `json:"energy" jsonschema:"enum=0,enum=25,enum=50,enum=75,enum=100"`
}

var suggestStyle = Declare[styleInput, styleAnswer](Spec[styleInput]{
	ID:           "suggest_style",
	Name:         "Suggestion de style",
	Description:  "Propose une voix de marque.",
	Instructions: "You suggest a brand voice.",
	Examples:     []styleInput{{Brand: "Synthiz"}},
})

var summarize = Declare[styleInput, string](Spec[styleInput]{
	ID:           "summarize",
	Description:  "Résume la marque.",
	Instructions: "You summarize.",
})

// model calls tool with args on its first request and answers text after.
func model(t *testing.T, tool, args, text string) (agent.Provider, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		write := func(v any) {
			b, _ := json.Marshal(v)
			_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
		}
		if n == 0 && tool != "" {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
				"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": tool, "arguments": args},
			}}}, "finish_reason": "tool_calls"}}})
		} else {
			write(map[string]any{"choices": []map[string]any{{"delta": map[string]any{"content": text}, "finish_reason": "stop"}}})
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return agent.Provider{BaseURL: srv.URL + "/v1", Model: "m"}, &bodies
}

func serve(t *testing.T, provider agent.Provider) (client.Agent, string) {
	t.Helper()
	h, err := host.New(context.Background(), host.Config{
		Agent:    host.Agent{Name: "partage", Instructions: "You are partage's writer."},
		Provider: provider,
		Token:    "tok",
		Skills:   Catalog(suggestStyle, summarize).Skills,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return client.Agent{URL: srv.URL, Token: "tok"}, srv.URL
}

func TestADeclarationCarriesItsSchemasAndExamples(t *testing.T) {
	d := suggestStyle.Declaration()
	if d.ID != "suggest_style" || !strings.Contains(string(d.Input), "What the brand's site says.") || !strings.Contains(string(d.Output), `"enum":[0,25,50,75,100]`) {
		t.Errorf("declaration = %+v\ninput %s\noutput %s", d, d.Input, d.Output)
	}
	if len(d.Examples) != 1 || string(d.Examples[0]) != `{"brand":"Synthiz"}` {
		t.Errorf("examples = %s", d.Examples)
	}
	if len(summarize.Declaration().Output) != 0 {
		t.Error("a text skill has no output schema")
	}
}

func TestRunSendsTheInputAsTheNamedSkillAndReturnsItsType(t *testing.T) {
	provider, bodies := model(t, "respond", `{"category":"devtools","energy":75}`, "")
	a, _ := serve(t, provider)

	got, turn, err := suggestStyle.Run(context.Background(), a, styleInput{Brand: "Synthiz"}, Call{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "devtools" || got.Energy != 75 {
		t.Errorf("got %+v", got)
	}
	if len(turn.Steps) != 1 || turn.Steps[0].Step != 1 || len(turn.Steps[0].ToolCalls) != 1 || turn.Steps[0].ToolCalls[0] != "respond" {
		t.Errorf("each model step must be reported: %+v", turn.Steps)
	}
	sent := (*bodies)[0]
	for _, want := range []string{"You are partage's writer.", "You suggest a brand voice.", `{\"brand\":\"Synthiz\"}`, `"enum":[0,25,50,75,100]`} {
		if !strings.Contains(sent, want) {
			t.Errorf("the model was not sent %q: %s", want, sent)
		}
	}
}

func TestATextSkillAnswersInText(t *testing.T) {
	provider, _ := model(t, "", "", "Une marque de veille.")
	a, _ := serve(t, provider)
	got, _, err := summarize.Run(context.Background(), a, styleInput{Brand: "Synthiz"}, Call{})
	if err != nil || got != "Une marque de veille." {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestAnInputOutsideItsSchemaIsRefusedBeforeAnyModelCall(t *testing.T) {
	provider, bodies := model(t, "", "", "x")
	a, _ := serve(t, provider)
	_, _, err := client.Say(context.Background(), a, client.Request{Text: `{"brand":42}`, Skill: "suggest_style"})
	if err == nil || !strings.Contains(err.Error(), "does not match its schema") {
		t.Fatalf("err = %v", err)
	}
	if len(*bodies) != 0 {
		t.Errorf("the model was called %d times for a refused input", len(*bodies))
	}
}

func TestAnUndeclaredSkillIsRefused(t *testing.T) {
	provider, _ := model(t, "", "", "x")
	a, _ := serve(t, provider)
	_, _, err := client.Say(context.Background(), a, client.Request{Text: "{}", Skill: "launch_rocket"})
	if err == nil || !strings.Contains(err.Error(), `declares no skill "launch_rocket"`) {
		t.Errorf("err = %v", err)
	}
}

func TestTheCardAndSkillsListTheDeclaredSkills(t *testing.T) {
	provider, _ := model(t, "", "", "x")
	_, url := serve(t, provider)

	res, err := http.Get(url + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var card struct {
		Skills []struct {
			ID          string   `json:"id"`
			Tags        []string `json:"tags"`
			Examples    []string `json:"examples"`
			OutputModes []string `json:"outputModes"`
		} `json:"skills"`
	}
	_ = json.NewDecoder(res.Body).Decode(&card)
	if len(card.Skills) < 2 || card.Skills[0].ID != "suggest_style" || card.Skills[0].Tags[0] != "skill" ||
		card.Skills[0].Examples[0] != `{"brand":"Synthiz"}` || card.Skills[0].OutputModes[0] != "application/json" {
		t.Errorf("skills = %+v", card.Skills)
	}

	res3, err := http.Get(url + "/.well-known/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res3.Body.Close()
	var ext struct {
		Capabilities struct {
			Extensions []struct {
				URI string `json:"uri"`
			} `json:"extensions"`
		} `json:"capabilities"`
	}
	_ = json.NewDecoder(res3.Body).Decode(&ext)
	if len(ext.Capabilities.Extensions) != 1 || ext.Capabilities.Extensions[0].URI != host.ExtensionURI {
		t.Errorf("the card must declare cortex's A2A extension: %+v", ext.Capabilities)
	}

	res2, err := http.Get(url + "/skills")
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var catalog host.Catalog
	_ = json.NewDecoder(res2.Body).Decode(&catalog)
	if len(catalog.Skills) != 2 || catalog.Skills[0].Instructions != "You suggest a brand voice." || len(catalog.Skills[0].Output) == 0 {
		t.Errorf("catalog = %+v", catalog)
	}
}

func TestWriteFileWritesTheCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills.json")
	if err := WriteFile(path, suggestStyle, summarize); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var c host.Catalog
	if err := json.Unmarshal(raw, &c); err != nil || len(c.Skills) != 2 || c.Skills[1].ID != "summarize" {
		t.Errorf("catalog %s: %v", raw, err)
	}
}

func TestDeclaringTwiceTheSameSkillIsRefusedByTheAgent(t *testing.T) {
	provider, _ := model(t, "", "", "x")
	_, err := host.New(context.Background(), host.Config{
		Agent: host.Agent{Name: "p"}, Provider: provider, Token: "t",
		Skills: Catalog(suggestStyle, suggestStyle).Skills,
	})
	if err == nil {
		t.Error("a skill declared twice was accepted")
	}
}

func TestSkillsSetAfterStartAreListedAndRun(t *testing.T) {
	provider, _ := model(t, "respond", `{"category":"devtools","energy":50}`, "")
	h, err := host.New(context.Background(), host.Config{Agent: host.Agent{Name: "partage"}, Provider: provider, Token: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	a := client.Agent{URL: srv.URL, Token: "tok"}

	if _, _, err := suggestStyle.Run(context.Background(), a, styleInput{Brand: "Synthiz"}, Call{}); err == nil {
		t.Fatal("a skill ran before it was declared")
	}
	if err := h.SetSkills(Catalog(suggestStyle).Skills); err != nil {
		t.Fatal(err)
	}
	if len(h.Card().Skills) != 1 || h.Card().Skills[0].ID != "suggest_style" {
		t.Errorf("card skills = %+v", h.Card().Skills)
	}
	got, _, err := suggestStyle.Run(context.Background(), a, styleInput{Brand: "Synthiz"}, Call{})
	if err != nil || got.Energy != 50 {
		t.Errorf("got %+v, %v", got, err)
	}
	if err := h.SetSkills(Catalog(suggestStyle, suggestStyle).Skills); err == nil {
		t.Error("an invalid catalog replaced the skills")
	}
}

func TestARunAsksForItsOwnModel(t *testing.T) {
	provider, bodies := model(t, "respond", `{"category":"devtools","energy":25}`, "")
	a, _ := serve(t, provider)
	if _, _, err := suggestStyle.Run(context.Background(), a, styleInput{Brand: "Synthiz"}, Call{Model: "qwen3-235b-a22b-instruct-2507"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*bodies)[0], `"model":"qwen3-235b-a22b-instruct-2507"`) {
		t.Errorf("the run's model was not asked for: %s", (*bodies)[0])
	}

	own, ownBodies := model(t, "respond", `{"category":"devtools","energy":25}`, "")
	b, _ := serve(t, own)
	if _, _, err := suggestStyle.Run(context.Background(), b, styleInput{Brand: "Synthiz"}, Call{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*ownBodies)[0], `"model":"m"`) {
		t.Errorf("without a model the agent's own is asked for: %s", (*ownBodies)[0])
	}
}
