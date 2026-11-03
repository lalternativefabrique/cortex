package host

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAskRunsATurnFromInsideTheProcess(t *testing.T) {
	provider, bodies := scriptedModel(t, scriptedReply{text: "Deux pages à corriger."})
	h, err := New(context.Background(), Config{Agent: Agent{Name: "ego", Instructions: "Tu aides."}, Provider: provider, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	answer, err := h.Ask(context.Background(), "marie", "task:seo-monday", "Vérifie le classement.")
	if err != nil || answer != "Deux pages à corriger." {
		t.Fatalf("answer = %q, err = %v", answer, err)
	}
	if !strings.Contains((*bodies)[0], "Vérifie le classement.") {
		t.Fatalf("the model was not asked the prompt:\n%s", (*bodies)[0])
	}
	answer, err = h.Ask(context.Background(), "marie", "task:seo-monday", "Et cette semaine ?")
	if err != nil || !strings.Contains((*bodies)[1], "Vérifie le classement.") {
		t.Fatalf("the second run lost the conversation: err = %v, body = %s", err, (*bodies)[1])
	}
}

func TestAskReportsAFailedTurn(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"no such model"}`, http.StatusBadRequest)
	}))
	defer down.Close()
	provider, _ := scriptedModel(t)
	provider.BaseURL = down.URL + "/v1"
	h, err := New(context.Background(), Config{Agent: Agent{Name: "ego"}, Provider: provider, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.Ask(context.Background(), "", "task:x", "bonjour"); err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("err = %v", err)
	}
}
