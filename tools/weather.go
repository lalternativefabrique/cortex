package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lalternative/packages/go/cortex/agent"
)

// Forecaster reads a daily forecast at coordinates.
type Forecaster interface {
	Forecast(ctx context.Context, lat, lon float64, days int) (*Forecast, error)
}

// WeatherConfig configures the weather tool.
type WeatherConfig struct {
	// Geocoder resolves the place name to coordinates. Nil leaves the tool
	// out: without it there is nothing to forecast for.
	Geocoder Geocoder
	// Forecaster reads the forecast. Nil leaves the tool out.
	Forecaster Forecaster
}

// Configured reports whether both halves are present.
func (c WeatherConfig) Configured() bool {
	return c.Geocoder != nil && c.Forecaster != nil
}

type weatherArgs struct {
	Place string `json:"place" jsonschema:"description=The place to forecast for — a town\\, address or landmark\\, as the user named it."`
	Days  int    `json:"days,omitempty" jsonschema:"description=How many days ahead\\, today included. Defaults to a week\\, at most 16."`
}

type weatherTool struct {
	cfg WeatherConfig
}

// NewWeather returns a tool that reads a place's forecast.
func NewWeather(cfg WeatherConfig) agent.Tool {
	return &weatherTool{cfg: cfg}
}

func (t *weatherTool) Name() string { return "weather" }

func (t *weatherTool) Description() string {
	return strings.Join([]string{
		"Get the daily weather forecast for a place: temperatures, precipitation, wind and conditions.",
		"",
		"Use this for any weather question rather than web_search. A weather site renders its forecast client-side, so searching one returns links and reading one returns the page's navigation — neither carries the numbers.",
		"",
		"Answers for the coordinates the place name resolves to, and reports the altitude it used: in mountains a valley forecast says nothing about conditions higher up.",
	}, "\n")
}

func (t *weatherTool) InputSchema() any { return weatherArgs{} }

func (t *weatherTool) Execute(ctx context.Context, raw json.RawMessage) (agent.ToolResult, error) {
	var args weatherArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return failure("could not parse arguments: %v", err)
	}
	place := strings.TrimSpace(args.Place)
	if place == "" {
		return failure("place is required")
	}
	if !t.cfg.Configured() {
		return failure("no weather backend is configured")
	}

	located, ok := t.cfg.Geocoder.Geocode(ctx, place)
	if !ok {
		return failure("could not locate %q", place)
	}

	forecast, err := t.cfg.Forecaster.Forecast(ctx, located.Lat, located.Lon, args.Days)
	if err != nil {
		return failure("%v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Forecast for %s (%.0f m, %s)\n\n", located.Label, forecast.ElevationM, forecast.Timezone)
	for _, day := range forecast.Days {
		fmt.Fprintf(&b, "%s: %s, %.0f to %.0f °C", day.Date, day.Condition, day.MinC, day.MaxC)
		if day.PrecipMM > 0 {
			fmt.Fprintf(&b, ", %.1f mm", day.PrecipMM)
			if day.PrecipChance > 0 {
				fmt.Fprintf(&b, " (%d%%)", day.PrecipChance)
			}
		}
		if day.WindMaxKMH > 0 {
			fmt.Fprintf(&b, ", wind to %.0f km/h", day.WindMaxKMH)
		}
		b.WriteString("\n")
	}

	return agent.ToolResult{
		Content: b.String(),
		Metadata: map[string]any{
			"ok": true, "place": located.Label,
			"lat": located.Lat, "lon": located.Lon,
			"elevation_m": forecast.ElevationM, "days": len(forecast.Days),
		},
	}, nil
}
