package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Searcher looks up a query on the web.
type Searcher interface {
	Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
}

// SearchQuery is one search, as the tools ask for it. It is a struct rather
// than arguments so a backend that grows a capability does not change every
// caller, and one that lacks it ignores the field instead of failing.
type SearchQuery struct {
	Query string
	// Scope says what kind of answer is wanted, not which engine to ask.
	// Empty is ScopeWeb.
	Scope SearchScope
	// MaxResults <= 0 leaves the backend's own default.
	MaxResults int
	// WithContent asks for the first results' pages to be read into the
	// answer, which saves a fetch per result. A backend that cannot do it
	// returns results without text, which is a poorer answer and not an
	// error.
	WithContent int
	// ContentRunes bounds each page read by WithContent. Zero leaves the
	// backend's default.
	ContentRunes int
}

// SearchScope is what a search is for. It names the intent — studies,
// recency, the open web — and never the categories or engines a backend
// happens to sort them into.
type SearchScope string

const (
	// ScopeWeb searches everything the backend has, which is the default and
	// what an unqualified question deserves.
	ScopeWeb SearchScope = ""
	// ScopeStudies restricts to scholarly sources: papers, journals,
	// preprints. A question about what research says is answered badly by a
	// blog post that summarises it.
	ScopeStudies SearchScope = "studies"
	// ScopeRecent restricts to the last weeks. What happened lately is
	// exactly what a general ranking buries under years of older pages.
	ScopeRecent SearchScope = "recent"
	// ScopeLocal skips scholarly sources entirely, for a query that
	// structurally cannot have a scholarly answer — a bakery nearby, a
	// site-scoped lookup — where they only add noise.
	ScopeLocal SearchScope = "local"
)

// Geocoder resolves a place name to coordinates and finds nearby points of
// interest.
type Geocoder interface {
	Geocode(ctx context.Context, query string) (place GeocodedPlace, ok bool)
	FindPlaces(ctx context.Context, tag string, center GeocodedPlace) []POI
}

// ReviewedPlaceFinder finds places with customer reviews. France-only: a non-French local search finds nothing here
// and falls back to Geocoder's worldwide, review-less results.
type ReviewedPlaceFinder interface {
	FindPlaces(ctx context.Context, query string) []ReviewedPlace
}

// WebSearchConfig configures the web_search tool.
type WebSearchConfig struct {
	Searcher Searcher
	// Geocoder is optional: when nil, a locally-scoped query never gets a
	// map, same as the deployment simply not having geocoding configured.
	Geocoder Geocoder
	// ReviewedPlaces is optional and tried before Geocoder for a local
	// query: when nil, or when it finds nothing (any non-French search),
	// Geocoder is used instead.
	ReviewedPlaces ReviewedPlaceFinder
}

type webSearchArgs struct {
	Query      string `json:"query" jsonschema:"description=What to search for."`
	MaxResults int    `json:"max_results,omitempty" jsonschema:"description=How many results to return. Defaults to a handful."`
	Scope      string `json:"scope,omitempty" jsonschema:"description=What kind of answer this needs. 'studies' for scholarly sources — papers\\, journals\\, preprints — when the question is about what research shows. 'recent' when what matters is the last few weeks and a general ranking would bury it under older pages. Leave empty to search everything\\, which is right for most questions.,enum=studies,enum=recent"`
	// LocalBusinessTag is filled in by the model, not looked up from a
	// keyword list — the model already knows OSM's tag vocabulary, so
	// asking it to name the tag scales to any business/service without a
	// hardcoded list going stale.
	LocalBusinessTag string `json:"local_business_tag,omitempty" jsonschema:"description=If this search is for a local business or service near the user (a plumber, a bakery, a doctor...), its OpenStreetMap tag as key=value — e.g. shop=plumber, shop=bakery, amenity=restaurant, healthcare=dentist. Leave empty otherwise."`
}

type webSearchTool struct {
	cfg WebSearchConfig
}

// NewWebSearch returns a tool that searches the web through cfg.Searcher.
func NewWebSearch(cfg WebSearchConfig) agent.Tool {
	return &webSearchTool{cfg: cfg}
}

func (t *webSearchTool) Name() string { return "web_search" }

func (t *webSearchTool) Description() string {
	return strings.Join([]string{
		"Search the web and get back a list of results: title, URL, and a short excerpt.",
		"",
		"Use this for anything the workspace does not have the answer to — current events, a library's current API, an error message from a dependency, a fact to check.",
		"",
		"Does not fetch or read the pages themselves — only what the search engine's excerpt shows. Open a promising result with fetch_url when the excerpt is too thin to answer from.",
	}, "\n")
}

func (t *webSearchTool) InputSchema() any { return webSearchArgs{} }

func (t *webSearchTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args webSearchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	if strings.TrimSpace(args.Query) == "" {
		return failure("query is required")
	}
	if t.cfg.Searcher == nil {
		return failure("no search backend is configured")
	}

	tag := strings.TrimSpace(args.LocalBusinessTag)
	isLocal := tag != ""

	scope := SearchScope(strings.TrimSpace(args.Scope))
	if isLocal {
		// A local business query has no scholarly answer — say so, whatever
		// the model asked for, so they cannot bury the relevant results.
		scope = ScopeLocal
	}
	results, err := t.cfg.Searcher.Search(ctx, SearchQuery{
		Query:      args.Query,
		Scope:      scope,
		MaxResults: args.MaxResults,
	})
	if err != nil {
		return failure("%v", err)
	}
	if len(results) == 0 {
		return agent.ToolResult{
			Content:  fmt.Sprintf("No results for %q.", args.Query),
			Metadata: map[string]any{"ok": true, "results": 0},
		}, nil
	}

	mapShown := false
	var geocoded GeocodedPlace
	geocodedOK := false
	if isLocal && t.cfg.Geocoder != nil {
		geocoded, geocodedOK = t.cfg.Geocoder.Geocode(ctx, args.Query)
	}
	if geocodedOK {
		results = filterByLocality(results, geocoded.Label)
	}

	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n\n", i+1, r.Title, r.URL, r.Content)
	}

	metadata := map[string]any{"ok": true, "query": args.Query, "results": len(results), "items": results, "local": isLocal}
	if isLocal {
		metadata["category"] = tag
	}

	if geocodedOK {
		metadata["location"] = geocoded
		mapShown = true

		var places []Place
		if t.cfg.ReviewedPlaces != nil {
			if reviewed := t.cfg.ReviewedPlaces.FindPlaces(ctx, args.Query); len(reviewed) > 0 {
				places = placesFromMappy(reviewed, tag)
			}
		}
		if len(places) == 0 {
			if osm := t.cfg.Geocoder.FindPlaces(ctx, tag, geocoded); len(osm) > 0 {
				places = placesFromOSM(osm)
			}
		}
		if len(places) > 0 {
			metadata["places"] = places
		}
	}

	if mapShown {
		b.WriteString("A map with these places is already shown to the user above your reply — do not restate the list. Give a one-sentence summary and, if useful, a recommendation, then ask what they'd like to know more about.")
	}

	return agent.ToolResult{
		Content:  strings.TrimRight(b.String(), "\n"),
		Metadata: metadata,
	}, nil
}

// Place is one business or point of interest shown to the user, whatever
// found it. Provider says which source it came from, so a caller can tell a
// mappy hit (reviews, opening status) from an OSM one (no reviews, sparser
// tagging) without inferring it from which fields happen to be set.
type Place struct {
	Provider      string  `json:"provider"`
	Name          string  `json:"name"`
	Lat           float64 `json:"lat"`
	Lon           float64 `json:"lon"`
	Address       string  `json:"address,omitempty"`
	Phone         string  `json:"phone,omitempty"`
	Website       string  `json:"website,omitempty"`
	Category      string  `json:"category,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
	MaxRating     float64 `json:"max_rating,omitempty"`
	ReviewCount   int     `json:"review_count,omitempty"`
	OpeningStatus string  `json:"opening_status,omitempty"`
	OpeningHours  string  `json:"opening_hours,omitempty"`
}

const (
	providerMappy = "mappy"
	providerOSM   = "osm"
)

// placesFromMappy carries the searched tag onto each hit: mappy resolves the
// business type from the query text and never returns it, so the tag the
// model named is the only category available for these.
func placesFromMappy(in []ReviewedPlace, tag string) []Place {
	out := make([]Place, 0, len(in))
	for _, p := range in {
		out = append(out, Place{
			Provider:      providerMappy,
			Name:          p.Name,
			Lat:           p.Lat,
			Lon:           p.Lon,
			Address:       p.Address,
			Phone:         p.Phone,
			Category:      tag,
			Rating:        p.Rating,
			MaxRating:     p.MaxRating,
			ReviewCount:   p.ReviewCount,
			OpeningStatus: p.OpeningStatus,
			OpeningHours:  p.OpeningHours,
		})
	}
	return out
}

func placesFromOSM(in []POI) []Place {
	out := make([]Place, 0, len(in))
	for _, p := range in {
		out = append(out, Place{
			Provider:     providerOSM,
			Name:         p.Name,
			Lat:          p.Lat,
			Lon:          p.Lon,
			Address:      p.Address,
			Phone:        p.Phone,
			Website:      p.Website,
			Category:     p.Category,
			OpeningHours: p.OpeningHours,
		})
	}
	return out
}

// filterByLocality drops results whose title and content don't mention the
// geocoded place's town — a general search engine has no notion of "near the
// user", so a national directory page ranking well for the business type
// alone (e.g. a "hairdressers in Nantes" listicle for a "hairdresser in Pau"
// search) can outrank pages actually about the resolved town. When every
// result gets dropped (the town name never appears verbatim, e.g. it's
// referred to by a nearby landmark instead), results passes through
// unfiltered rather than surfacing nothing.
func filterByLocality(results []SearchResult, placeLabel string) []SearchResult {
	town, _, _ := strings.Cut(placeLabel, ",")
	town = strings.ToLower(strings.TrimSpace(town))
	if town == "" {
		return results
	}

	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		haystack := strings.ToLower(r.Title + " " + r.Content)
		if strings.Contains(haystack, town) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) == 0 {
		return results
	}
	return filtered
}
