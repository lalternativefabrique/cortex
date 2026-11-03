package profile

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lalternativefabrique/cortex/agent"
)

var (
	day   = 24 * time.Hour
	start = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	marie = Scope{Subject: "marie"}
)

func TestRenderGroupsByFacetNewestFirstAndSkipsClosedFacts(t *testing.T) {
	closed := start.Add(day)
	out := Render([]Fact{
		{Facet: Tone, Text: "vouvoiement", Since: start, Until: &closed},
		{Facet: Tone, Text: "tutoiement, phrases courtes", Since: start.Add(day)},
		{Facet: Context, Text: "vend des agents B2B", Since: start},
		{Facet: Context, Text: "trois produits: cortext, Partage, Synthiz", Since: start.Add(2 * day)},
	}, 0)
	want := renderHeader + "\n- context: trois produits: cortext, Partage, Synthiz\n- context: vend des agents B2B\n- tone: tutoiement, phrases courtes"
	if out != want {
		t.Fatalf("rendered:\n%s\nwant:\n%s", out, want)
	}
	if Render([]Fact{{Facet: Tone, Text: "x", Until: &closed}}, 0) != "" {
		t.Fatal("a profile with no valid fact must render nothing")
	}
}

func TestRenderStopsAtTheByteBudget(t *testing.T) {
	var facts []Fact
	for i := 0; i < 50; i++ {
		facts = append(facts, Fact{Facet: Preference, Text: strings.Repeat("x", 40), Since: start.Add(time.Duration(i) * time.Minute)})
	}
	out := Render(facts, 400)
	if len(out) > 400 || strings.Count(out, "\n- ") == 0 {
		t.Fatalf("len = %d, lines = %d", len(out), strings.Count(out, "\n- "))
	}
}

type memStore struct {
	signals []Signal
	facts   []Fact
}

func (m *memStore) Observe(_ context.Context, s Signal) error {
	m.signals = append(m.signals, s)
	return nil
}
func (m *memStore) Signals(_ context.Context, scope Scope, since time.Time, limit int) ([]Signal, error) {
	var out []Signal
	for i := len(m.signals) - 1; i >= 0 && len(out) < limit; i-- {
		if s := m.signals[i]; s.Scope == scope && !s.At.Before(since) {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *memStore) Facts(_ context.Context, scope Scope) ([]Fact, error) {
	var out []Fact
	for _, f := range m.facts {
		if f.Scope == scope && f.Valid() {
			out = append(out, f)
		}
	}
	return out, nil
}
func (m *memStore) Assert(_ context.Context, f Fact) error { m.facts = append(m.facts, f); return nil }
func (m *memStore) Retract(_ context.Context, scope Scope, id string, at time.Time) error {
	for i := range m.facts {
		if m.facts[i].Scope == scope && m.facts[i].ID == id && m.facts[i].Until == nil {
			m.facts[i].Until = &at
		}
	}
	return nil
}
func (m *memStore) Forget(context.Context, Scope) error { return nil }

type scriptedClient struct {
	calls []agent.CompletionRequest
	reply agent.CompletionResponse
}

func (c *scriptedClient) Complete(_ context.Context, req agent.CompletionRequest) (agent.CompletionResponse, error) {
	c.calls = append(c.calls, req)
	return c.reply, nil
}

func updateReply(args string) agent.CompletionResponse {
	return agent.CompletionResponse{ToolCalls: []agent.ToolCall{{ID: "u1", Name: updateName, Arguments: json.RawMessage(args)}}}
}

func TestLearnAssertsAndRetractsWhatTheModelReturns(t *testing.T) {
	store := &memStore{
		facts: []Fact{{ID: "f-vous", Scope: marie, Facet: Tone, Text: "vouvoiement", Source: Did, Since: start}},
	}
	for _, s := range []Signal{
		{Scope: marie, ID: "s1", Kind: SignalSaid, Content: "tutoie-moi, et fais court", At: start.Add(day)},
		{Scope: Scope{Subject: "paul"}, ID: "s2", Kind: SignalSaid, Content: "vouvoyez-moi", At: start.Add(day)},
	} {
		_ = store.Observe(context.Background(), s)
	}
	client := &scriptedClient{reply: updateReply(`{"assert":[{"facet":"tone","text":"tutoiement, phrases courtes","source":"said"},{"facet":"mood","text":"ignored"}],"retract":["f-vous","unknown"]}`)}
	d := &Deriver{Client: client, Store: store, Now: func() time.Time { return start.Add(2 * day) }}

	changed, err := d.Learn(context.Background(), marie)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 2 {
		t.Fatalf("changed = %d", changed)
	}
	prompt := client.calls[0].Messages[0].Content
	if !strings.Contains(prompt, "[f-vous] tone: vouvoiement") || !strings.Contains(prompt, "tutoie-moi") || strings.Contains(prompt, "vouvoyez-moi") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	valid, _ := store.Facts(context.Background(), marie)
	if len(valid) != 1 || valid[0].Text != "tutoiement, phrases courtes" || valid[0].Source != Said || valid[0].Facet != Tone {
		t.Fatalf("valid facts = %+v", valid)
	}
	if store.facts[0].Until == nil {
		t.Fatal("the contradicted fact was not closed")
	}
}

func TestLearnIsQuietWithoutSignalsAndCapsEachFacet(t *testing.T) {
	store := &memStore{}
	client := &scriptedClient{reply: updateReply(`{"assert":[{"facet":"preference","text":"a"},{"facet":"preference","text":"b"},{"facet":"preference","text":"A"}]}`)}
	d := &Deriver{Client: client, Store: store, MaxPerFacet: 1, Now: func() time.Time { return start }}

	if changed, err := d.Learn(context.Background(), marie); err != nil || changed != 0 || len(client.calls) != 0 {
		t.Fatalf("without signals: changed = %d, calls = %d, err = %v", changed, len(client.calls), err)
	}
	_ = store.Observe(context.Background(), Signal{Scope: marie, ID: "s1", Kind: SignalSaid, Content: "hello", At: start})
	changed, err := d.Learn(context.Background(), marie)
	if err != nil || changed != 1 {
		t.Fatalf("changed = %d, err = %v", changed, err)
	}
	if valid, _ := store.Facts(context.Background(), marie); len(valid) != 1 || valid[0].Text != "a" || valid[0].Source != Did {
		t.Fatalf("valid facts = %+v", valid)
	}
}

func TestFileStoreKeepsSignalsAndFactsAndForgets(t *testing.T) {
	ctx := context.Background()
	store := NewFileStoreAt(t.TempDir())
	for i, text := range []string{"old", "recent", "newest"} {
		if err := store.Observe(ctx, Signal{Scope: marie, ID: text, Kind: SignalSaid, Content: text, At: start.Add(time.Duration(i) * day)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = store.Observe(ctx, Signal{Scope: Scope{Subject: "paul"}, ID: "p", Kind: SignalSaid, Content: "paul", At: start.Add(5 * day)})
	got, err := store.Signals(ctx, marie, start.Add(day), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "newest" || got[1].ID != "recent" {
		t.Fatalf("signals = %+v", got)
	}
	if got, _ := store.Signals(ctx, marie, start, 1); len(got) != 1 || got[0].ID != "newest" {
		t.Fatalf("limited signals = %+v", got)
	}

	if err := store.Assert(ctx, Fact{ID: "f1", Scope: marie, Facet: Tone, Text: "vouvoiement", Source: Did, Since: start}); err != nil {
		t.Fatal(err)
	}
	_ = store.Assert(ctx, Fact{ID: "f2", Scope: marie, Facet: Tone, Text: "tutoiement", Source: Said, Since: start.Add(day)})
	if err := store.Retract(ctx, marie, "f1", start.Add(2*day)); err != nil {
		t.Fatal(err)
	}
	valid, _ := store.Facts(ctx, marie)
	if len(valid) != 1 || valid[0].ID != "f2" {
		t.Fatalf("valid = %+v", valid)
	}
	all, _ := store.replay(marie)
	if len(all) != 2 || all[0].Until == nil || !all[0].Until.Equal(start.Add(2*day)) {
		t.Fatalf("replayed = %+v", all)
	}

	if err := store.Forget(ctx, marie); err != nil {
		t.Fatal(err)
	}
	if valid, _ := store.Facts(ctx, marie); len(valid) != 0 {
		t.Fatalf("after forget = %+v", valid)
	}
	if got, _ := store.Signals(ctx, Scope{Subject: "paul"}, start, 10); len(got) != 1 {
		t.Fatal("forgetting marie must not touch paul")
	}
}
