package fetchpage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lalternative/packages/go/cortex/tools"
)

func TestFetchReadsThroughTornad(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Tornad-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"title": "Eysus", "text": "22 °C demain"})
	}))
	defer srv.Close()

	page, err := New(Config{BaseURL: srv.URL, Key: "k"}).Fetch(context.Background(), tools.FetchRequest{URL: "https://example.test/a", MaxRunes: 1200, Render: true})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/fetch" {
		t.Errorf("path is %q, want /fetch — /render returns raw HTML, losing the extraction tornad already did", gotPath)
	}
	if gotKey != "k" {
		t.Errorf("key header is %q, want k", gotKey)
	}
	if gotBody["render"] != true {
		t.Errorf("render is %v, want true — a JS-only page yields nothing without it", gotBody["render"])
	}
	if gotBody["max_runes"] != float64(1200) {
		t.Errorf("max_runes is %v, want 1200", gotBody["max_runes"])
	}
	if page.Title != "Eysus" || page.Text != "22 °C demain" {
		t.Errorf("got %q / %q, want Eysus / 22 °C demain", page.Title, page.Text)
	}
}

func TestFetchDefaultsMaxRunes(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"title": "t", "text": "x"})
	}))
	defer srv.Close()

	if _, err := New(Config{BaseURL: srv.URL}).Fetch(context.Background(), tools.FetchRequest{URL: "https://example.test/a", MaxRunes: 0, Render: true}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotBody["max_runes"] != float64(DefaultMaxRunes) {
		t.Errorf("max_runes is %v, want %d", gotBody["max_runes"], DefaultMaxRunes)
	}
}

func TestFetchSendsNoKeyHeaderWhenUnset(t *testing.T) {
	var hadKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadKey = r.Header[http.CanonicalHeaderKey("X-Tornad-Key")]
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"title": "t", "text": "x"})
	}))
	defer srv.Close()

	if _, err := New(Config{BaseURL: srv.URL}).Fetch(context.Background(), tools.FetchRequest{URL: "https://example.test/a", MaxRunes: 0, Render: true}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hadKey {
		t.Error("key header was sent with no Key configured")
	}
}

func TestFetchReportsVvavesFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked by challenge", http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := New(Config{BaseURL: srv.URL}).Fetch(context.Background(), tools.FetchRequest{URL: "https://example.test/a", MaxRunes: 0, Render: true})
	if err == nil {
		t.Fatal("Fetch succeeded on a 502, want an error the model can act on")
	}
}

func TestFetchExtractsLocallyWithoutVvaves(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Local</title></head><body><article>` +
			`<p>Un paragraphe assez long pour que readability le retienne comme corps de page.</p>` +
			`<p>Un second paragraphe, pour que l'extraction ait de quoi travailler.</p>` +
			`</article></body></html>`))
	}))
	defer page.Close()

	got, err := New(Config{}).Fetch(context.Background(), tools.FetchRequest{URL: page.URL})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.Text == "" {
		t.Error("local extraction yielded no text")
	}
}

// A page tornad could not read (502) is routine on the open web — a publisher
// refusing a datacenter address, a page that never settles. Retrying it is
// pointless, but a static read from here may still succeed where a rendered
// one did not, so the client falls through to local extraction instead of
// failing the caller.
func TestFetchFallsBackWhenTornadCannotRead(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Eysus</title></head><body><article><p>` +
			strings.Repeat("Le village tient sur un versant. ", 20) + `</p></article></body></html>`))
	}))
	defer origin.Close()

	var calls int
	tornad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream refused"}`))
	}))
	defer tornad.Close()

	page, err := New(Config{BaseURL: tornad.URL}).Fetch(context.Background(), tools.FetchRequest{URL: origin.URL, MaxRunes: 0, Render: true})
	if err != nil {
		t.Fatalf("Fetch: %v — an unreadable page should fall back, not fail", err)
	}
	if calls != 1 {
		t.Errorf("tornad was called %d times, want 1 — a refused page is not retried", calls)
	}
	if !strings.Contains(page.Text, "versant") {
		t.Errorf("text is %q, want the locally extracted article", page.Text)
	}
}

// A tornad that is genuinely down is not a page refusing to be read: the
// caller hears about it rather than silently getting a degraded answer.
func TestFetchDoesNotFallBackOnOutage(t *testing.T) {
	tornad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer tornad.Close()

	if _, err := New(Config{BaseURL: tornad.URL}).Fetch(context.Background(), tools.FetchRequest{URL: "https://example.test/a", MaxRunes: 0, Render: true}); err == nil {
		t.Fatal("an outage was swallowed by the local fallback")
	}
}
