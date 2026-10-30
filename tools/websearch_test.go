package tools

import (
	"testing"
)

func TestFilterByLocalityDropsResultsFromOtherTowns(t *testing.T) {
	results := []SearchResult{
		{Title: "Top 10 des coiffeurs à Pau", Content: "Trouvez votre coiffeur à Pau"},
		{Title: "Top 10 des coiffeurs à Nantes", Content: "Coiffeurs pour femmes et hommes"},
	}

	out := filterByLocality(results, "Pau, Pyrénées-Atlantiques, Nouvelle-Aquitaine, France")

	if len(out) != 1 {
		t.Fatalf("expected 1 result, got %d: %+v", len(out), out)
	}
	if out[0].Title != "Top 10 des coiffeurs à Pau" {
		t.Fatalf("wrong result kept: %+v", out[0])
	}
}

func TestFilterByLocalityFallsBackWhenNothingMatches(t *testing.T) {
	results := []SearchResult{
		{Title: "Salon Excellence", Content: "Coiffeur près de la mairie"},
	}

	out := filterByLocality(results, "Pau, Pyrénées-Atlantiques, France")

	if len(out) != 1 {
		t.Fatalf("expected fallback to unfiltered results, got %d: %+v", len(out), out)
	}
}

func TestFilterByLocalityWithEmptyLabelReturnsUnfiltered(t *testing.T) {
	results := []SearchResult{{Title: "Anything"}}

	out := filterByLocality(results, "")

	if len(out) != 1 {
		t.Fatalf("expected unfiltered passthrough, got %d", len(out))
	}
}
