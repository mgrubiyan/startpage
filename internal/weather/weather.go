// Package weather fetches current conditions and a short daily forecast from
// Open-Meteo (no API key required).
package weather

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// ErrCityNotFound is returned when geocoding finds no match.
var ErrCityNotFound = errors.New("city not found")

// ForecastDays is how many days Current asks for, today included.
const ForecastDays = 5

// Report is the current weather at a resolved location, with a daily forecast.
type Report struct {
	Location     string
	TemperatureC float64
	FeelsLikeC   float64
	WindMS       float64
	Code         int // WMO weather interpretation code
	Description  string
	Icon         string
	// UTCOffset is the location's offset from UTC; Day.Date, Sunrise and Sunset
	// are wall-clock values there, not where the server runs.
	UTCOffset time.Duration
	// Days starts with today at the location and is in date order.
	Days []Day
}

// Day is the forecast for one calendar day at the location.
type Day struct {
	Date        string // YYYY-MM-DD
	MinC        float64
	MaxC        float64
	Code        int
	Description string
	Icon        string
	// PrecipitationPct is the chance of precipitation, or nil if the model gives none.
	PrecipitationPct *int
	// Sunrise and Sunset are zero when the sun doesn't rise or set that day
	// (polar night, midnight sun).
	Sunrise time.Time
	Sunset  time.Time
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
	UTCOffsetSeconds int `json:"utc_offset_seconds"`
	// Parallel arrays, one element per day. The model can leave a gap as null.
	Daily *struct {
		Time          []string   `json:"time"`
		WeatherCode   []*int     `json:"weather_code"`
		TempMax       []*float64 `json:"temperature_2m_max"`
		TempMin       []*float64 `json:"temperature_2m_min"`
		Precipitation []*int     `json:"precipitation_probability_max"`
		Sunrise       []string   `json:"sunrise"`
		Sunset        []string   `json:"sunset"`
	} `json:"daily"`
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
		"daily":           {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,sunrise,sunset"},
		"forecast_days":   {fmt.Sprint(ForecastDays)},
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

	days, err := fc.days()
	if err != nil {
		return Report{}, fmt.Errorf("forecast for %q: %w", city, err)
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
		UTCOffset:    time.Duration(fc.UTCOffsetSeconds) * time.Second,
		Days:         days,
	}, nil
}

// localMinute is how Open-Meteo writes sunrise and sunset: wall-clock time at
// the location, without an offset.
const localMinute = "2006-01-02T15:04"

// days turns the parallel daily arrays into Days. A day the model has no
// temperatures or weather code for is left out; arrays of different lengths
// mean the response isn't what was asked for.
func (fc *forecastResponse) days() ([]Day, error) {
	d := fc.Daily
	if d == nil {
		return nil, errors.New("response has no daily block")
	}
	n := len(d.Time)
	if len(d.WeatherCode) != n || len(d.TempMax) != n || len(d.TempMin) != n ||
		len(d.Precipitation) != n || len(d.Sunrise) != n || len(d.Sunset) != n {
		return nil, errors.New("daily arrays differ in length")
	}
	loc := time.FixedZone("", fc.UTCOffsetSeconds)
	days := make([]Day, 0, n)
	for i, date := range d.Time {
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			return nil, fmt.Errorf("daily date %q: %w", date, err)
		}
		if d.WeatherCode[i] == nil || d.TempMax[i] == nil || d.TempMin[i] == nil {
			continue
		}
		desc, icon := Describe(*d.WeatherCode[i])
		day := Day{
			Date:             date,
			MinC:             *d.TempMin[i],
			MaxC:             *d.TempMax[i],
			Code:             *d.WeatherCode[i],
			Description:      desc,
			Icon:             icon,
			PrecipitationPct: d.Precipitation[i],
		}
		// For a day without a sunrise the API repeats one time (midnight) in both
		// fields, so anything but a sunset after the sunrise means "no sun events".
		rise, errRise := time.ParseInLocation(localMinute, d.Sunrise[i], loc)
		set, errSet := time.ParseInLocation(localMinute, d.Sunset[i], loc)
		if errRise == nil && errSet == nil && set.After(rise) {
			day.Sunrise, day.Sunset = rise, set
		}
		days = append(days, day)
	}
	return days, nil
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
