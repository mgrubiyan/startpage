// Package weather fetches current conditions from Open-Meteo (no API key required).
package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// ErrCityNotFound is returned when geocoding finds no match.
var ErrCityNotFound = errors.New("city not found")

// Report is the current weather at a resolved location.
type Report struct {
	Location     string
	TemperatureC float64
	FeelsLikeC   float64
	WindMS       float64
	Code         int // WMO weather interpretation code
	Description  string
	Icon         string
}

// Client talks to the Open-Meteo geocoding and forecast APIs.
type Client struct {
	http        *http.Client
	geocodeURL  string
	forecastURL string
}

// New returns a Client using the public Open-Meteo endpoints.
func New(hc *http.Client) *Client {
	return &Client{
		http:        hc,
		geocodeURL:  "https://geocoding-api.open-meteo.com/v1/search",
		forecastURL: "https://api.open-meteo.com/v1/forecast",
	}
}

type geocodeResponse struct {
	Results []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"results"`
}

type forecastResponse struct {
	Current *struct {
		Temperature float64 `json:"temperature_2m"`
		FeelsLike   float64 `json:"apparent_temperature"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
}

// Current resolves city to coordinates and returns the weather there.
func (c *Client) Current(ctx context.Context, city string) (Report, error) {
	q := url.Values{"name": {city}, "count": {"1"}, "language": {"ru"}}
	var geo geocodeResponse
	if err := fetch.JSON(ctx, c.http, c.geocodeURL+"?"+q.Encode(), &geo); err != nil {
		return Report{}, fmt.Errorf("geocode %q: %w", city, err)
	}
	if len(geo.Results) == 0 {
		return Report{}, fmt.Errorf("geocode %q: %w", city, ErrCityNotFound)
	}
	place := geo.Results[0]

	q = url.Values{
		"latitude":        {fmt.Sprint(place.Latitude)},
		"longitude":       {fmt.Sprint(place.Longitude)},
		"current":         {"temperature_2m,apparent_temperature,weather_code,wind_speed_10m"},
		"wind_speed_unit": {"ms"},
		"timezone":        {"auto"},
	}
	var fc forecastResponse
	if err := fetch.JSON(ctx, c.http, c.forecastURL+"?"+q.Encode(), &fc); err != nil {
		return Report{}, fmt.Errorf("forecast for %q: %w", city, err)
	}
	if fc.Current == nil {
		return Report{}, fmt.Errorf("forecast for %q: response has no current block", city)
	}

	desc, icon := Describe(fc.Current.WeatherCode)
	return Report{
		Location:     place.Name,
		TemperatureC: fc.Current.Temperature,
		FeelsLikeC:   fc.Current.FeelsLike,
		WindMS:       fc.Current.WindSpeed,
		Code:         fc.Current.WeatherCode,
		Description:  desc,
		Icon:         icon,
	}, nil
}

var wmoGroups = []struct {
	codes []int
	desc  string
	icon  string
}{
	{[]int{0}, "ясно", "☀️"},
	{[]int{1, 2}, "малооблачно", "🌤️"},
	{[]int{3}, "пасмурно", "☁️"},
	{[]int{45, 48}, "туман", "🌫️"},
	{[]int{51, 53, 55, 56, 57}, "морось", "🌦️"},
	{[]int{61, 63, 65, 66, 67, 80, 81, 82}, "дождь", "🌧️"},
	{[]int{71, 73, 75, 77, 85, 86}, "снег", "🌨️"},
	{[]int{95, 96, 99}, "гроза", "⛈️"},
}

// Describe maps a WMO weather code to a short Russian description and an emoji.
func Describe(code int) (desc, icon string) {
	for _, g := range wmoGroups {
		if slices.Contains(g.codes, code) {
			return g.desc, g.icon
		}
	}
	return "", "🌡️"
}
