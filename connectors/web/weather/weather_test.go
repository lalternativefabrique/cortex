package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const bodyTwoDays = `{
  "elevation": 282.0,
  "timezone": "Europe/Paris",
  "daily": {
    "time": ["2026-09-14", "2026-09-15"],
    "weather_code": [3, 53],
    "temperature_2m_max": [32.7, 20.0],
    "temperature_2m_min": [12.5, 16.6],
    "precipitation_sum": [0.0, 3.47],
    "precipitation_probability_max": [0, 71],
    "wind_speed_10m_max": [11.2, 24.8]
  }
}`

func serving(t *testing.T, body string, seen *string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.URL.RawQuery
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(0)
	c.httpClient = srv.Client()
	c.httpClient.Transport = rewrite{to: srv.URL, base: srv.Client().Transport}
	return c
}

// rewrite sends every request to the test server, whatever host the client
// built the URL for.
type rewrite struct {
	to   string
	base http.RoundTripper
}

func (r rewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := http.NewRequest(req.Method, r.to+"?"+req.URL.RawQuery, nil)
	if err != nil {
		return nil, err
	}
	target = target.WithContext(req.Context())
	base := r.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(target)
}

func TestForecastReadsDays(t *testing.T) {
	got, err := serving(t, bodyTwoDays, nil).Forecast(context.Background(), 43.1833, -0.5833, 2)
	if err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if got.ElevationM != 282 {
		t.Errorf("elevation is %v, want 282 — a mountain forecast is meaningless without the altitude it is for", got.ElevationM)
	}
	if len(got.Days) != 2 {
		t.Fatalf("got %d days, want 2", len(got.Days))
	}
	first := got.Days[0]
	if first.Date != "2026-09-14" || first.MaxC != 32.7 || first.MinC != 12.5 {
		t.Errorf("first day is %+v, want 2026-09-14 12.5..32.7", first)
	}
	if first.Condition != "overcast" {
		t.Errorf("condition is %q, want overcast for code 3", first.Condition)
	}
	second := got.Days[1]
	if second.PrecipMM != 3.47 || second.PrecipChance != 71 || second.WindMaxKMH != 24.8 {
		t.Errorf("second day is %+v, want 3.47 mm / 71%% / 24.8 km/h", second)
	}
}

func TestForecastRequestsTheLocalDay(t *testing.T) {
	var query string
	if _, err := serving(t, bodyTwoDays, &query).Forecast(context.Background(), 43.1833, -0.5833, 3); err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if !strings.Contains(query, "timezone=auto") {
		t.Errorf("query %q lacks timezone=auto — dates would then be UTC days, not the place's own", query)
	}
	if !strings.Contains(query, "forecast_days=3") {
		t.Errorf("query %q lacks forecast_days=3", query)
	}
}

func TestForecastClampsDays(t *testing.T) {
	var query string
	if _, err := serving(t, bodyTwoDays, &query).Forecast(context.Background(), 1, 2, 99); err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if !strings.Contains(query, "forecast_days=16") {
		t.Errorf("query %q does not clamp to 16, which is all Open-Meteo serves", query)
	}
}

func TestForecastDefaultsDays(t *testing.T) {
	var query string
	if _, err := serving(t, bodyTwoDays, &query).Forecast(context.Background(), 1, 2, 0); err != nil {
		t.Fatalf("Forecast: %v", err)
	}
	if !strings.Contains(query, "forecast_days=7") {
		t.Errorf("query %q does not default to 7 days", query)
	}
}

func TestForecastRejectsEmptyDaily(t *testing.T) {
	_, err := serving(t, `{"elevation":10,"timezone":"UTC","daily":{"time":[]}}`, nil).
		Forecast(context.Background(), 1, 2, 3)
	if err == nil {
		t.Fatal("Forecast succeeded with no days, want an error rather than an empty answer read as a forecast")
	}
}

func TestForecastReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(0)
	c.httpClient = &http.Client{Transport: rewrite{to: srv.URL}}

	if _, err := c.Forecast(context.Background(), 1, 2, 3); err == nil {
		t.Fatal("Forecast succeeded on a 503")
	}
}

func TestConditionNamesUnknownCodeHonestly(t *testing.T) {
	if got := Condition(0); got != "clear" {
		t.Errorf("Condition(0) is %q, want clear", got)
	}
	if got := Condition(95); got != "thunderstorm" {
		t.Errorf("Condition(95) is %q, want thunderstorm", got)
	}
	if got := Condition(1234); got != "weather code 1234" {
		t.Errorf("Condition(1234) is %q, want it to name the code rather than invent a condition", got)
	}
}
