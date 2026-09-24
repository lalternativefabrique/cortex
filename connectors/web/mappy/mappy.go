// Package mappy finds nearby places with customer reviews through
// mappy.com — France's only reviewed-business directory reachable without
// a Google API key (mappy.com re-serves PagesJaunes/Solocal data).
//
// This is France-only: mappy has no useful coverage outside it, so
// web_search falls back to geocoding.FindPlaces (Overpass, worldwide, no
// reviews) when mappy finds nothing.
//
// mappy.com renders its result data into a client-side
// window.__PRELOADED_STATE__ blob rather than exposing it as JSON-LD or an
// API — an internal React state shape, not a documented contract, so
// FindPlaces reads it defensively and returns nothing rather than erroring
// when a field is missing or the shape has changed.
package mappy

import (
	"context"
	"encoding/json"
	"github.com/lalternative/packages/go/cortex/tools"
	"regexp"
	"strings"
	"time"
)

const (
	// renderTimeout bounds one page render — mappy's client-side hydration
	// takes noticeably longer than a static fetch.
	renderTimeout = 15 * time.Second
	// maxPlaces caps how many pins a map shows.
	maxPlaces = 15
)

// Searcher looks up a query on the web. *websearch.Client implements it.
type Searcher interface {
	Search(ctx context.Context, q tools.SearchQuery) ([]tools.SearchResult, error)
}

// Renderer renders a URL's JavaScript and returns the resulting HTML.
// *rendersvc.Client (github.com/lalternative/packages/go/search/rendersvc)
// implements it.
type Renderer interface {
	Render(ctx context.Context, url string, timeout time.Duration) (html string, err error)
}

// POI is the kernel's reviewed place.
type POI = tools.ReviewedPlace

// Client finds places on mappy.com.
type Client struct {
	searcher Searcher
	renderer Renderer
}

// New returns a Client. Both dependencies are required: without a searcher
// there is no mappy URL to render, and without a renderer there is no way
// to reach mappy's client-rendered state.
func New(searcher Searcher, renderer Renderer) *Client {
	return &Client{searcher: searcher, renderer: renderer}
}

// FindPlaces looks up query on mappy.com and returns the places found near
// its first matching result, or nil when nothing matched, the page didn't
// render, or its state couldn't be parsed — callers treat that as "no
// reviewed places to show", not an error worth surfacing to the model.
//
// The first mappy.com result is often a category/area page (e.g. "Garage
// Biarritz") rather than the specific business the query names, so its POIs
// are filtered against any proper-noun tokens in query — a business name a
// generic "garages in <town>" query never has — to avoid surfacing unrelated
// places under a specific business's name.
func (c *Client) FindPlaces(ctx context.Context, query string) []tools.ReviewedPlace {
	results, err := c.searcher.Search(ctx, tools.SearchQuery{
		// A mappy listing has no scholarly counterpart, and the query is
		// site-scoped anyway: scholarly sources would only add noise.
		Query:      "site:mappy.com " + query,
		Scope:      tools.ScopeLocal,
		MaxResults: 3,
	})
	if err != nil {
		return nil
	}

	var entryURL string
	for _, r := range results {
		if strings.Contains(r.URL, "mappy.com") {
			entryURL = r.URL
			break
		}
	}
	if entryURL == "" {
		return nil
	}

	html, err := c.renderer.Render(ctx, entryURL, renderTimeout)
	if err != nil {
		return nil
	}

	places := parsePreloadedState(html)
	return filterByRelevance(places, query)
}

// properNounTokens returns the words in query that look like they name a
// specific business rather than a place type or town — capitalized words at
// least 3 runes long, skipping common French stopwords that are capitalized
// only by sentence position.
var stopwords = map[string]bool{
	"le": true, "la": true, "les": true, "de": true, "des": true, "du": true,
	"un": true, "une": true, "et": true, "à": true, "au": true, "aux": true,
	"trouve": true, "trouver": true, "cherche": true, "chercher": true,
	"garage": true, "garages": true, "restaurant": true, "restaurants": true,
	"bar": true, "bars": true, "café": true, "cafés": true, "coiffeur": true,
	"coiffeurs": true, "pharmacie": true, "pharmacies": true, "médecin": true,
	"médecins": true, "dentiste": true, "dentistes": true, "boulangerie": true,
	"boulangeries": true, "hôtel": true, "hôtels": true,
}

func properNounTokens(query string) []string {
	var tokens []string
	for _, w := range strings.Fields(query) {
		w = strings.Trim(w, ".,;:!?'\"()")
		if len([]rune(w)) < 3 {
			continue
		}
		if stopwords[strings.ToLower(w)] {
			continue
		}
		r := []rune(w)[0]
		if !(r >= 'A' && r <= 'Z') && !strings.ContainsRune("ÀÂÄÉÈÊËÎÏÔÖÙÛÜÇ", r) {
			continue
		}
		tokens = append(tokens, w)
	}
	return tokens
}

// filterByRelevance drops POIs whose name doesn't contain any proper-noun
// token from query. When query names no specific business (a generic "shop
// type in town" search), every token is a stopword or the town's own name,
// so no tokens survive and places passes through unfiltered — the mappy
// page is already scoped to the right category and area.
func filterByRelevance(places []POI, query string) []tools.ReviewedPlace {
	tokens := properNounTokens(query)
	if len(tokens) == 0 {
		return places
	}

	filtered := make([]POI, 0, len(places))
	for _, p := range places {
		name := strings.ToLower(p.Name)
		for _, t := range tokens {
			if strings.Contains(name, strings.ToLower(t)) {
				filtered = append(filtered, p)
				break
			}
		}
	}
	if len(filtered) == 0 {
		return places
	}
	return filtered
}

var preloadedStateStart = regexp.MustCompile(`window\.__PRELOADED_STATE__\s*=\s*\{`)

type preloadedState struct {
	Geoentity struct {
		Pois []mappyPOI `json:"pois"`
	} `json:"geoentity"`
}

type mappyPOI struct {
	Name        string `json:"name"`
	Way         string `json:"way"`
	Town        string `json:"town"`
	Postcode    string `json:"postcode"`
	Coordinates struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	} `json:"coordinates"`
	Communication struct {
		Phone struct {
			Number string `json:"number"`
		} `json:"phone"`
	} `json:"communication"`
	Reviews struct {
		AverageNote     float64 `json:"averageNote"`
		MaxNote         float64 `json:"maxNote"`
		NumberOfReviews int     `json:"numberOfReviews"`
	} `json:"reviews"`
	OpeningHours  string `json:"openingHours"`
	OpeningStatus struct {
		Status string `json:"status"`
	} `json:"openingStatus"`
}

// parsePreloadedState extracts window.__PRELOADED_STATE__ from a rendered
// mappy.com page and converts its POIs. Any parse failure yields nil, not an
// error: this reads an undocumented, unstable client state shape, so a
// mappy deploy that changes it should silently disable the map, not break
// web_search.
func parsePreloadedState(html string) []POI {
	loc := preloadedStateStart.FindStringIndex(html)
	if loc == nil {
		return nil
	}
	// loc[1]-1 is the opening '{': walk from there, tracking brace and string
	// nesting, to find the matching close — a regex can't balance nested
	// braces, and this JSON embeds free-form text (addresses, names) that
	// may itself contain '{' or '}'.
	object := extractJSONObject(html[loc[1]-1:])
	if object == "" {
		return nil
	}

	var state preloadedState
	if err := json.Unmarshal([]byte(object), &state); err != nil {
		return nil
	}

	pois := state.Geoentity.Pois
	if len(pois) > maxPlaces {
		pois = pois[:maxPlaces]
	}

	places := make([]POI, 0, len(pois))
	for _, p := range pois {
		if p.Name == "" || (p.Coordinates.Lat == 0 && p.Coordinates.Lng == 0) {
			continue
		}
		places = append(places, POI{
			Name:          p.Name,
			Address:       strings.TrimSpace(strings.Join([]string{p.Way, p.Postcode, p.Town}, " ")),
			Lat:           p.Coordinates.Lat,
			Lon:           p.Coordinates.Lng,
			Phone:         p.Communication.Phone.Number,
			Rating:        p.Reviews.AverageNote,
			MaxRating:     p.Reviews.MaxNote,
			ReviewCount:   p.Reviews.NumberOfReviews,
			OpeningStatus: p.OpeningStatus.Status,
			OpeningHours:  p.OpeningHours,
		})
	}
	return places
}

// extractJSONObject returns the balanced {...} object starting at s[0], or
// "" if s doesn't start with '{' or the braces never balance (truncated
// input). Tracks string literals so a brace inside a quoted value — an
// address or business name in this data — isn't mistaken for structure.
func extractJSONObject(s string) string {
	if len(s) == 0 || s[0] != '{' {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i, r := range s {
		if inString {
			switch {
			case escaped:
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[:i+1]
			}
		}
	}
	return ""
}
