package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lalternativefabrique/cortex/tools"
)

func TestConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"neither", Config{}, false},
		{"blank", Config{TornadURL: "  ", BaseURL: "\t"}, false},
		{"tornade only", Config{TornadURL: "http://tornade"}, true},
		{"searxng only", Config{BaseURL: "http://searxng"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Configured(); got != tc.want {
				t.Errorf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNewWithoutAnyBackendFails(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New(empty) returned nil error")
	}
}

// A tornade in the config must be what answers: it fuses the categories and
// backs the general one with Brave, neither of which a direct SearXNG call
// gets. Reaching SearXNG while a tornade is configured is the regression.
func TestSearchPrefersTornade(t *testing.T) {
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("SearXNG was called while a tornade was configured")
	}))
	defer searxng.Close()

	var body map[string]any
	tornade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"query": "gramsci",
			"results": []map[string]any{
				{"title": "Quaderni", "url": "https://example.org/q", "description": "notes", "source": "brave"},
			},
		})
	}))
	defer tornade.Close()

	client, err := New(Config{TornadURL: tornade.URL, BaseURL: searxng.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	results, err := client.Search(context.Background(), tools.SearchQuery{Query: "gramsci", MaxResults: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Title != "Quaderni" {
		t.Fatalf("results = %+v, want the tornade hit", results)
	}
	if results[0].Engine != "brave" {
		t.Errorf("Engine = %q, want brave — the source must survive the mapping", results[0].Engine)
	}

	categories, _ := body["categories"].([]any)
	if len(categories) != 2 {
		t.Fatalf("categories = %v, want both handed to tornade at once", categories)
	}
}

// SearchGeneral exists to keep academic engines out of a query that cannot
// have an academic answer, so the category list must narrow accordingly.
func TestSearchGeneralSendsOnlyGeneral(t *testing.T) {
	var body map[string]any
	tornade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"query": "bakery", "results": []map[string]any{}})
	}))
	defer tornade.Close()

	client, err := New(Config{TornadURL: tornade.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Search(context.Background(), tools.SearchQuery{Query: "bakery near me", MaxResults: 5, Scope: tools.ScopeLocal}); err != nil {
		t.Fatalf("SearchGeneral: %v", err)
	}

	categories, _ := body["categories"].([]any)
	if len(categories) != 1 || categories[0] != "general" {
		t.Errorf("categories = %v, want [general] only", categories)
	}
}

// The two calls must not share backing state: SearchGeneral slicing the
// category list must not leave Search with a shortened one.
func TestSearchAfterSearchGeneralStillSendsBoth(t *testing.T) {
	var last map[string]any
	tornade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = nil
		json.NewDecoder(r.Body).Decode(&last)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"query": "x", "results": []map[string]any{}})
	}))
	defer tornade.Close()

	client, _ := New(Config{TornadURL: tornade.URL})
	if _, err := client.Search(context.Background(), tools.SearchQuery{Query: "bakery", MaxResults: 5, Scope: tools.ScopeLocal}); err != nil {
		t.Fatalf("SearchGeneral: %v", err)
	}
	if _, err := client.Search(context.Background(), tools.SearchQuery{Query: "gramsci", MaxResults: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	categories, _ := last["categories"].([]any)
	if len(categories) != 2 {
		t.Errorf("categories = %v, want both after an earlier general-only call", categories)
	}
}

func TestSearchFallsBackToSearxngWithoutTornade(t *testing.T) {
	var called bool
	searxng := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{}})
	}))
	defer searxng.Close()

	client, err := New(Config{BaseURL: searxng.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Search(context.Background(), tools.SearchQuery{Query: "gramsci", MaxResults: 5}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !called {
		t.Error("SearXNG was never called with no tornade configured")
	}
}

func TestSearchRecentRetriesWithoutTimeRangeWhenEmpty(t *testing.T) {
	var ranges []any
	tornade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		ranges = append(ranges, body["time_range"])
		results := []map[string]any{}
		if body["time_range"] == nil {
			results = append(results, map[string]any{"title": "Smic", "url": "https://www.service-public.gouv.fr/particuliers/vosdroits/F2300"})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"query": "smic", "results": results})
	}))
	defer tornade.Close()

	client, _ := New(Config{TornadURL: tornade.URL})
	got, err := client.Search(context.Background(), tools.SearchQuery{Query: "smic 2026", MaxResults: 5, Scope: tools.ScopeRecent})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(ranges) != 2 || ranges[0] != "month" || ranges[1] != nil {
		t.Errorf("time ranges sent = %v, want [month <none>]", ranges)
	}
	if len(got) != 1 {
		t.Errorf("results = %d, want the unranged search's 1", len(got))
	}
}

func TestSearchRecentKeepsTimeRangedResults(t *testing.T) {
	calls := 0
	tornade := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"query": "x", "results": []map[string]any{{"title": "fresh", "url": "https://example.org/fresh"}}})
	}))
	defer tornade.Close()

	client, _ := New(Config{TornadURL: tornade.URL})
	if _, err := client.Search(context.Background(), tools.SearchQuery{Query: "x", MaxResults: 5, Scope: tools.ScopeRecent}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 when the time-ranged search found something", calls)
	}
}
