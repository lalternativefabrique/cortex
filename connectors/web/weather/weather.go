// Package weather reads a forecast from Open-Meteo.
//
// A weather page is a grid of widgets its site renders client-side, which
// readability extracts nothing usable from: searching and reading one is how
// a forecast question comes back as a list of links. Open-Meteo answers the
// same question as JSON, with no API key and no per-call cost.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lalternative/packages/go/cortex/tools"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	// DefaultTimeout bounds one forecast call.
	DefaultTimeout = 10 * time.Second
	// DefaultDays is a week ahead, past which a daily forecast is a
	// climatology rather than a forecast.
	DefaultDays = 7
	// maxDays is what Open-Meteo serves.
	maxDays = 16

	baseURL = "https://api.open-meteo.com/v1/forecast"
)

// Day is the kernel's forecast day.
type Day = tools.ForecastDay

// Forecast is the kernel's forecast.
type Forecast = tools.Forecast

// Client reads forecasts.
type Client struct {
	httpClient *http.Client
}

// New returns a Client. Zero timeout uses DefaultTimeout.
func New(timeout time.Duration) *Client {
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Client{httpClient: &http.Client{Timeout: timeout}}
}

type response struct {
	Elevation float64 `json:"elevation"`
	Timezone  string  `json:"timezone"`
	Daily     struct {
		Time              []string  `json:"time"`
		TempMax           []float64 `json:"temperature_2m_max"`
		TempMin           []float64 `json:"temperature_2m_min"`
		Precipitation     []float64 `json:"precipitation_sum"`
		PrecipProbability []int     `json:"precipitation_probability_max"`
		WindMax           []float64 `json:"wind_speed_10m_max"`
		WeatherCode       []int     `json:"weather_code"`
	} `json:"daily"`
}

// Forecast reads the daily forecast at lat/lon for the next days, clamped to
// what Open-Meteo serves. Times are resolved in the location's own zone, so a
// date is the day it is there rather than in UTC.
func (c *Client) Forecast(ctx context.Context, lat, lon float64, days int) (*tools.Forecast, error) {
	if days <= 0 {
		days = DefaultDays
	}
	if days > maxDays {
		days = maxDays
	}

	q := url.Values{
		"latitude":      {strconv.FormatFloat(lat, 'f', 4, 64)},
		"longitude":     {strconv.FormatFloat(lon, 'f', 4, 64)},
		"daily":         {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_sum,precipitation_probability_max,wind_speed_10m_max"},
		"timezone":      {"auto"},
		"forecast_days": {strconv.Itoa(days)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("weather: build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weather: call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("weather: status %d", resp.StatusCode)
	}

	var body response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("weather: read body: %w", err)
	}

	out := &Forecast{ElevationM: body.Elevation, Timezone: body.Timezone}
	for i, date := range body.Daily.Time {
		day := Day{Date: date}
		if i < len(body.Daily.TempMax) {
			day.MaxC = body.Daily.TempMax[i]
		}
		if i < len(body.Daily.TempMin) {
			day.MinC = body.Daily.TempMin[i]
		}
		if i < len(body.Daily.Precipitation) {
			day.PrecipMM = body.Daily.Precipitation[i]
		}
		if i < len(body.Daily.PrecipProbability) {
			day.PrecipChance = body.Daily.PrecipProbability[i]
		}
		if i < len(body.Daily.WindMax) {
			day.WindMaxKMH = body.Daily.WindMax[i]
		}
		if i < len(body.Daily.WeatherCode) {
			day.Condition = Condition(body.Daily.WeatherCode[i])
		}
		out.Days = append(out.Days, day)
	}
	if len(out.Days) == 0 {
		return nil, fmt.Errorf("weather: no forecast for %.4f,%.4f", lat, lon)
	}
	return out, nil
}

// Condition names a WMO weather code (code 4677, the table Open-Meteo's
// weather_code follows). An unlisted code names itself rather than claiming a
// condition the table does not define.
func Condition(code int) string {
	switch code {
	case 0:
		return "clear"
	case 1:
		return "mainly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61, 63, 65:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75:
		return "snow"
	case 77:
		return "snow grains"
	case 80, 81, 82:
		return "rain showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm with hail"
	default:
		return fmt.Sprintf("weather code %d", code)
	}
}
