package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestAnswerFiguresReadsProseNumbers(t *testing.T) {
	got := answerFigures("Le SMIC net est de 1 477,93 € depuis le 1er juin 2026, soit 12,31 € de l'heure (20,84 %). Sources : W5, W7.")
	want := []string{"1 477,93", "2026", "12,31", "20,84"}
	if !slices.Equal(got, want) {
		t.Errorf("figures = %q, want %q", got, want)
	}
}

func TestCheckFiguresMatchesWhateverTheSeparators(t *testing.T) {
	page := "Au 1er juin 2026, le SMIC mensuel net est de 1477.93 euros, soit 12,31 € brut de l'heure."
	check := checkFigures("Il est de 1 477,93 € (12,31 € brut), depuis juin 2026.", []source{{Text: page}})
	if check.Checked != 3 || check.Sourced != 3 || len(check.Unsourced) != 0 {
		t.Errorf("check = %+v, want 3 of 3 sourced", check)
	}
}

func TestCheckFiguresReportsAnInventedFigure(t *testing.T) {
	check := checkFigures("Le SMIC brut est de 1 856,84 €.", []source{{Text: "Le SMIC mensuel brut est de 1 866,98 euros."}})
	if !slices.Equal(check.Unsourced, []string{"1 856,84"}) {
		t.Errorf("unsourced = %q, want [1 856,84]", check.Unsourced)
	}
}

func TestCheckFiguresDoesNotMatchInsideALongerNumber(t *testing.T) {
	check := checkFigures("Il y a 477 cas.", []source{{Text: "Le montant est de 1 477,93 €."}})
	if check.Sourced != 0 {
		t.Errorf("check = %+v, want 477 unsourced: it is part of 1 477,93", check)
	}
}

func webRun(t *testing.T, page string, answers ...string) (*scriptedClient, Result) {
	t.Helper()
	responses := []CompletionResponse{{ToolCalls: []ToolCall{{ID: "c1", Name: "fetch_url", Arguments: json.RawMessage(`{}`)}}}}
	for _, a := range answers {
		responses = append(responses, CompletionResponse{Text: a})
	}
	client := &scriptedClient{responses: responses}
	r, err := NewRunner(Config{Client: client, Tools: []Tool{&fakeTool{name: "fetch_url", result: page}}, SourceTools: []string{"fetch_url"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "Quel est le SMIC net ?"}})
	if err != nil {
		t.Fatal(err)
	}
	return client, res
}

func TestRunAsksOnceToCorrectUnsourcedFigures(t *testing.T) {
	client, res := webRun(t, "Le SMIC net est de 1 477,93 €.", "Il est de 1 400 €.", "Il est de 1 477,93 €.")
	if len(client.calls) != 3 {
		t.Fatalf("model calls = %d, want 3: tool, invented answer, corrected answer", len(client.calls))
	}
	sent := client.calls[2].Messages
	notice := sent[len(sent)-1]
	if notice.Role != RoleUser || !strings.Contains(notice.Content, "1 400") {
		t.Errorf("last message before the retry = %+v, want a notice naming 1 400", notice)
	}
	if res.Text != "Il est de 1 477,93 €." || res.Figures.Sourced != 1 || len(res.Figures.Unsourced) != 0 {
		t.Errorf("result = %q %+v, want the corrected answer, fully sourced", res.Text, res.Figures)
	}
}

func TestRunReportsFiguresStillUnsourcedAfterOneRetry(t *testing.T) {
	client, res := webRun(t, "Le SMIC net est de 1 477,93 €.", "Il est de 1 400 €.", "Il est de 1 400 € net.")
	if len(client.calls) != 3 {
		t.Fatalf("model calls = %d, want 3: one retry only", len(client.calls))
	}
	if !slices.Equal(res.Figures.Unsourced, []string{"1 400"}) {
		t.Errorf("unsourced = %q, want [1 400] reported", res.Figures.Unsourced)
	}
}

func TestRunWithoutSourceToolsChecksNothing(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{{Text: "Il est de 1 400 €."}}}
	r, _ := NewRunner(Config{Client: client})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "SMIC ?"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 1 || res.Figures.Checked != 0 {
		t.Errorf("calls = %d, figures = %+v, want one call and no check without a source read", len(client.calls), res.Figures)
	}
}

func TestRunCountsFiguresThePersonGave(t *testing.T) {
	client := &scriptedClient{responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "web_search", Arguments: json.RawMessage(`{}`)}}},
		{Text: "Avec 2 300 € brut, tu es au-dessus du SMIC (1 823,03 €)."},
	}}
	r, _ := NewRunner(Config{Client: client, Tools: []Tool{&fakeTool{name: "web_search", result: "SMIC brut : 1 823,03 €"}}, SourceTools: []string{"web_search"}})
	res, err := r.Run(context.Background(), []Message{{Role: RoleUser, Content: "Je gagne 2300 € brut, c'est plus que le SMIC ?"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 || res.Figures.Sourced != 2 {
		t.Errorf("calls = %d, figures = %+v, want no retry: 2300 came from the person", len(client.calls), res.Figures)
	}
}

func gazolePage() string {
	return `[{"ref":"W5","title":"Prix Aujourd'hui","url":"https://prixaujourdhui.com/","excerpt":"Samedi 24 octobre 2026 Gazole 2,372 €/L E10 2,136 €/L Livret A 1,70 %"}]`
}

func TestCheckFiguresPointsAtThePageAndPassage(t *testing.T) {
	check := checkFigures("Le gazole est à 2,372 €/L [W5].", sourcesOf(gazolePage()))
	if len(check.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want one", check.Evidence)
	}
	ev := check.Evidence[0]
	if ev.Ref != "W5" || ev.URL != "https://prixaujourdhui.com/" || !strings.Contains(ev.Passage, "Gazole 2,372 €/L") {
		t.Errorf("evidence = %+v, want W5 and the passage carrying 2,372", ev)
	}
}

func TestCheckFiguresAcceptsAShownCalculation(t *testing.T) {
	sources := append(sourcesOf(gazolePage()), source{Text: "Combien coûte un plein de 50 litres ?"})
	check := checkFigures("Un plein coûte 118,60 € (50 × 2,372 €) [W5].", sources)
	if len(check.Unsourced) != 0 || check.Sourced != 3 {
		t.Fatalf("check = %+v, want 118,60 accepted as 50 × 2,372", check)
	}
	if !slices.ContainsFunc(check.Evidence, func(e FigureEvidence) bool { return e.Figure == "118,60" && strings.Contains(e.Formula, "50 × 2,372") }) {
		t.Errorf("evidence = %+v, want the formula for 118,60", check.Evidence)
	}
}

func TestCheckFiguresRefusesAWrongCalculation(t *testing.T) {
	sources := append(sourcesOf(gazolePage()), source{Text: "un plein de 50 litres"})
	check := checkFigures("Un plein coûte 128,60 € (50 × 2,372 €).", sources)
	if !slices.Equal(check.Unsourced, []string{"128,60"}) {
		t.Errorf("unsourced = %q, want the wrong product refused", check.Unsourced)
	}
}

func TestCheckFiguresChecksTimes(t *testing.T) {
	check := checkFigures("Relevé à 11h00.", sourcesOf(gazolePage()))
	if !slices.Equal(check.Unsourced, []string{"11h00"}) {
		t.Errorf("unsourced = %q, want 11h00 reported: the page gives no time", check.Unsourced)
	}
	check = checkFigures("Relevé à 11h00.", []source{{Text: "mis à jour à 11:00"}})
	if check.Sourced != 1 {
		t.Errorf("check = %+v, want 11h00 found as 11:00", check)
	}
}

func TestCitationsListCitedPagesOnce(t *testing.T) {
	sources := sourcesOf(`[{"ref":"W1","title":"Service public","url":"https://sp.fr","excerpt":"Smic 1 477,93"},{"ref":"W2","title":"Blog","url":"https://blog.fr","excerpt":"autre"}]`)
	check := checkFigures("Il est de 1 477,93 € [W1], voir aussi [W1].", sources)
	got := citations("Il est de 1 477,93 € [W1], voir aussi [W1].", sources, check)
	if len(got) != 1 || got[0].Ref != "W1" || got[0].URL != "https://sp.fr" {
		t.Errorf("citations = %+v, want W1 once", got)
	}
}
