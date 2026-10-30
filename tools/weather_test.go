package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type stubGeocoder struct {
	place GeocodedPlace
	ok    bool
}

func (s stubGeocoder) Geocode(context.Context, string) (GeocodedPlace, bool) {
	return s.place, s.ok
}

func (s stubGeocoder) FindPlaces(context.Context, string, GeocodedPlace) []POI {
	return nil
}

type stubForecaster struct {
	forecast *Forecast
	err      error
	gotLat   float64
	gotLon   float64
	gotDays  int
}

func (s *stubForecaster) Forecast(_ context.Context, lat, lon float64, days int) (*Forecast, error) {
	s.gotLat, s.gotLon, s.gotDays = lat, lon, days
	return s.forecast, s.err
}

func runWeather(t *testing.T, cfg WeatherConfig, args string) string {
	t.Helper()
	res, err := NewWeather(cfg).Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res.Content
}

func TestWeatherForecastsWhereThePlaceResolved(t *testing.T) {
	forecaster := &stubForecaster{forecast: &Forecast{
		ElevationM: 282, Timezone: "Europe/Paris",
		Days: []ForecastDay{
			{Date: "2026-09-14", MinC: 12.5, MaxC: 32.7, Condition: "overcast"},
			{Date: "2026-09-15", MinC: 16.6, MaxC: 20, PrecipMM: 3.47, PrecipChance: 71, WindMaxKMH: 24.8, Condition: "drizzle"},
		},
	}}
	cfg := WeatherConfig{
		Geocoder:   stubGeocoder{place: GeocodedPlace{Lat: 43.18, Lon: -0.58, Label: "Eysus"}, ok: true},
		Forecaster: forecaster,
	}

	got := runWeather(t, cfg, `{"place":"Eysus","days":2}`)

	if forecaster.gotLat != 43.18 || forecaster.gotLon != -0.58 {
		t.Errorf("forecast asked for %v,%v, want the geocoded 43.18,-0.58", forecaster.gotLat, forecaster.gotLon)
	}
	if forecaster.gotDays != 2 {
		t.Errorf("forecast asked for %d days, want 2", forecaster.gotDays)
	}
	for _, want := range []string{"Eysus", "282 m", "2026-09-14", "12 to 33 °C", "overcast", "3.5 mm", "71%", "25 km/h"} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q:\n%s", want, got)
		}
	}
}

func TestWeatherReportsAnUnknownPlace(t *testing.T) {
	cfg := WeatherConfig{
		Geocoder:   stubGeocoder{ok: false},
		Forecaster: &stubForecaster{},
	}

	got := runWeather(t, cfg, `{"place":"Nowhere at all"}`)

	if !strings.Contains(got, "error") {
		t.Errorf("an unlocatable place answered %q, want an error rather than a forecast for somewhere else", got)
	}
}

func TestWeatherReportsAForecastFailure(t *testing.T) {
	cfg := WeatherConfig{
		Geocoder:   stubGeocoder{place: GeocodedPlace{Label: "Eysus"}, ok: true},
		Forecaster: &stubForecaster{err: fmt.Errorf("weather: status 503")},
	}

	got := runWeather(t, cfg, `{"place":"Eysus"}`)

	if !strings.Contains(got, "error") || !strings.Contains(got, "503") {
		t.Errorf("a failed forecast answered %q, want the failure reported", got)
	}
}

func TestWeatherNeedsAPlace(t *testing.T) {
	cfg := WeatherConfig{
		Geocoder:   stubGeocoder{ok: true},
		Forecaster: &stubForecaster{},
	}

	if got := runWeather(t, cfg, `{"place":"  "}`); !strings.Contains(got, "error") {
		t.Errorf("a blank place answered %q, want an error", got)
	}
}

func TestWeatherConfiguredNeedsBothHalves(t *testing.T) {
	if (WeatherConfig{Geocoder: stubGeocoder{}}).Configured() {
		t.Error("Configured with no Forecaster, want false — there is nothing to read a forecast from")
	}
	if (WeatherConfig{Forecaster: &stubForecaster{}}).Configured() {
		t.Error("Configured with no Geocoder, want false — a place name cannot be resolved")
	}
	if !(WeatherConfig{Geocoder: stubGeocoder{}, Forecaster: &stubForecaster{}}).Configured() {
		t.Error("Configured with both halves, want true")
	}
}
