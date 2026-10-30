package tools

// SearchResult is one web hit, as the tools return it to the model.
type SearchResult struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Content    string `json:"content"`
	Engine     string `json:"engine"`
	Favicon    string `json:"favicon,omitempty"`
	OGImage    string `json:"og_image,omitempty"`
	OGSiteName string `json:"og_site_name,omitempty"`
	// SourceType is the upstream provider's classification of the hit —
	// "web", "youtube" or "podcast" — so a caller can group results by
	// medium rather than presenting every hit as an undifferentiated link.
	SourceType  string `json:"source_type,omitempty"`
	Author      string `json:"author,omitempty"`
	PublishedAt string `json:"published_at,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`

	// Text and Markdown carry the result's page itself, and are filled only
	// when the search asked for content. Content above stays the excerpt.
	Text     string `json:"text,omitempty"`
	Markdown string `json:"markdown,omitempty"`
	// ContentError says why this result's page could not be read. The search
	// itself succeeded: one unreadable page does not fail the others.
	ContentError string `json:"content_error,omitempty"`
}

// Page is one web page's readable text.
type Page struct {
	Title string
	Text  string
}

// GeocodedPlace is the point on the map a place name resolved to. It is what
// a Geocoder answers, not what a tool shows: Place is the shape the model
// sees, whichever provider found it.
type GeocodedPlace struct {
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Label string  `json:"label"`
}

// POI is a point of interest near a GeocodedPlace.
type POI struct {
	Name         string  `json:"name"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	Address      string  `json:"address,omitempty"`
	Phone        string  `json:"phone,omitempty"`
	OpeningHours string  `json:"opening_hours,omitempty"`
	Website      string  `json:"website,omitempty"`
	Category     string  `json:"category,omitempty"`
}

// ReviewedPlace is a POI a provider also has customer reviews for.
type ReviewedPlace struct {
	Name          string  `json:"name"`
	Address       string  `json:"address"`
	Lat           float64 `json:"lat"`
	Lon           float64 `json:"lon"`
	Phone         string  `json:"phone,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
	MaxRating     float64 `json:"max_rating,omitempty"`
	ReviewCount   int     `json:"review_count,omitempty"`
	OpeningStatus string  `json:"opening_status,omitempty"`
	OpeningHours  string  `json:"opening_hours,omitempty"`
}

// ForecastDay is one day of a Forecast.
type ForecastDay struct {
	Date         string
	MinC         float64
	MaxC         float64
	PrecipMM     float64
	PrecipChance int
	WindMaxKMH   float64
	Condition    string
}

// Forecast is what one place's coordinates resolve to.
type Forecast struct {
	// ElevationM is the altitude the provider answered for, which in
	// mountains is what separates a valley reading from a summit one.
	ElevationM float64
	Timezone   string
	Days       []ForecastDay
}
