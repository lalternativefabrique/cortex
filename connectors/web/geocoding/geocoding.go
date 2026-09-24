// Package geocoding resolves a local web_search query to a map: Nominatim
// turns the query into a center point, then Overpass looks up nearby points
// of interest around it (a nearby garage, restaurant...).
//
// This is lalter-specific: the web_search tool decides when a query looks
// local, and which OSM tag it's after, and calls Geocode/FindPlaces on it.
// Neither service asks for an API key but Nominatim's usage policy requires a
// descriptive User-Agent and no more than one request per second — fine here
// since each is called at most once per web_search call, never in a loop.
package geocoding

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lalternative/packages/go/cortex/tools"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// endpoint is Nominatim's public instance. lalter runs no instance of its
	// own — geocoding is rare enough (one call per locally-scoped search)
	// that self-hosting isn't warranted.
	endpoint = "https://nominatim.openstreetmap.org/search"
	// overpassEndpoint is Overpass API's public instance, same rationale.
	overpassEndpoint = "https://overpass-api.de/api/interpreter"
	userAgent        = "lalter-assistant/1.0"
	DefaultTimeout   = 5 * time.Second
	// placesRadiusMeters bounds the Overpass search around the geocoded
	// center — wide enough to cover a town, narrow enough to stay local.
	placesRadiusMeters = 5000
	// maxPlaces caps how many pins a map shows — plenty for a local search's
	// map, without dumping every match in a dense area onto it.
	maxPlaces = 15
)

// Place is the kernel's geocoded place; this package answers in it.
type Place = tools.GeocodedPlace

// Client queries Nominatim.
type Client struct {
	httpClient *http.Client
}

// New returns a Client. A nil or zero-value Config uses DefaultTimeout.
func New(timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Client{httpClient: &http.Client{Timeout: timeout}}
}

type nominatimResult struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	DisplayName string `json:"display_name"`
}

// Geocode resolves query to its best-matching place, or ok=false when
// nothing matched or the lookup failed — callers treat that as "no map to
// show", not an error worth surfacing to the model.
func (c *Client) Geocode(ctx context.Context, query string) (place tools.GeocodedPlace, ok bool) {
	u := fmt.Sprintf("%s?%s", endpoint, url.Values{
		"q":      {query},
		"format": {"jsonv2"},
		"limit":  {"1"},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Place{}, false
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Place{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Place{}, false
	}

	var results []nominatimResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil || len(results) == 0 {
		return Place{}, false
	}

	lat, err := strconv.ParseFloat(results[0].Lat, 64)
	if err != nil {
		return Place{}, false
	}
	lon, err := strconv.ParseFloat(results[0].Lon, 64)
	if err != nil {
		return Place{}, false
	}

	return Place{Lat: lat, Lon: lon, Label: results[0].DisplayName}, true
}

// POI is the kernel's point of interest.
type POI = tools.POI

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
}

type overpassElement struct {
	Lat    float64           `json:"lat"`
	Lon    float64           `json:"lon"`
	Center *overpassCenter   `json:"center"`
	Tags   map[string]string `json:"tags"`
}

type overpassCenter struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// FindPlaces looks up named points of interest matching an OSM tag
// (key=value, e.g. "shop=car_repair") within placesRadiusMeters of center.
// Returns an empty, non-nil slice when nothing matched or the lookup
// failed — callers treat that as "no pins to show", not an error worth
// surfacing to the model.
func (c *Client) FindPlaces(ctx context.Context, tag string, center tools.GeocodedPlace) []tools.POI {
	key, value, ok := strings.Cut(tag, "=")
	if !ok {
		return []POI{}
	}

	query := fmt.Sprintf(
		`[out:json][timeout:10];(node["%s"="%s"](around:%d,%f,%f);way["%s"="%s"](around:%d,%f,%f););out center tags %d;`,
		key, value, placesRadiusMeters, center.Lat, center.Lon,
		key, value, placesRadiusMeters, center.Lat, center.Lon,
		maxPlaces,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, overpassEndpoint,
		strings.NewReader(url.Values{"data": {query}}.Encode()))
	if err != nil {
		return []POI{}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return []POI{}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return []POI{}
	}

	var parsed overpassResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return []POI{}
	}

	places := make([]POI, 0, len(parsed.Elements))
	for _, el := range parsed.Elements {
		name := el.Tags["name"]
		if name == "" {
			continue
		}
		lat, lon := el.Lat, el.Lon
		if el.Center != nil {
			lat, lon = el.Center.Lat, el.Center.Lon
		}
		places = append(places, POI{
			Name:         name,
			Lat:          lat,
			Lon:          lon,
			Address:      streetAddress(el.Tags),
			Phone:        firstTag(el.Tags, "phone", "contact:phone"),
			OpeningHours: el.Tags["opening_hours"],
			Website:      firstTag(el.Tags, "website", "contact:website"),
			Category:     tag,
		})
	}
	return places
}

// streetAddress assembles OSM's addr:* parts into one line. A place tagged
// with only a city and no street yields the city alone rather than nothing.
func streetAddress(tags map[string]string) string {
	var street string
	switch {
	case tags["addr:housenumber"] != "" && tags["addr:street"] != "":
		street = tags["addr:housenumber"] + " " + tags["addr:street"]
	default:
		street = tags["addr:street"]
	}

	town := strings.TrimSpace(tags["addr:postcode"] + " " + firstTag(tags, "addr:city", "addr:town", "addr:village"))

	parts := make([]string, 0, 2)
	for _, p := range []string{street, town} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}

func firstTag(tags map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := tags[k]; v != "" {
			return v
		}
	}
	return ""
}
