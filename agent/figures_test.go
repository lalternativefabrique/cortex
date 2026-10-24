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
	source := "Au 1er juin 2026, le SMIC mensuel net est de 1477.93 euros, soit 12,31 € brut de l'heure."
	check := checkFigures("Il est de 1 477,93 € (12,31 € brut), depuis juin 2026.", []string{source})
	if check.Checked != 3 || check.Sourced != 3 || len(check.Unsourced) != 0 {
		t.Errorf("check = %+v, want 3 of 3 sourced", check)
	}
}

func TestCheckFiguresReportsAnInventedFigure(t *testing.T) {
	check := checkFigures("Le SMIC brut est de 1 856,84 €.", []string{"Le SMIC mensuel brut est de 1 866,98 euros."})
	if !slices.Equal(check.Unsourced, []string{"1 856,84"}) {
		t.Errorf("unsourced = %q, want [1 856,84]", check.Unsourced)
	}
}

func TestCheckFiguresDoesNotMatchInsideALongerNumber(t *testing.T) {
	check := checkFigures("Il y a 477 cas.", []string{"Le montant est de 1 477,93 €."})
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
