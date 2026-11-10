package weather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

func newTestClient(t *testing.T, geocode, forecast http.HandlerFunc) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/geo", geocode)
	mux.HandleFunc("/fc", forecast)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.geocodeURL, c.forecastURL = srv.URL+"/geo", srv.URL+"/fc"
	return c
}

func TestCurrent(t *testing.T) {
	var gotCity, gotUA string
	var gotQuery url.Values
	c := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotCity, gotUA = r.URL.Query().Get("name"), r.UserAgent()
			_, _ = w.Write([]byte(`{"results":[{"name":"Санкт-Петербург","latitude":59.93863,"longitude":30.31413}]}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			_, _ = w.Write([]byte(`{"utc_offset_seconds":10800,
				"current":{"temperature_2m":1.6,"apparent_temperature":-2.4,"weather_code":3,"wind_speed_10m":4.2},
				"daily":{"time":["2026-11-09","2026-11-10"],"weather_code":[3,71],
					"temperature_2m_max":[8.2,-0.5],"temperature_2m_min":[6.6,-3.3],
					"precipitation_probability_max":[36,null],
					"sunrise":["2026-11-09T08:40","2026-11-10T08:43"],"sunset":["2026-11-09T16:43","2026-11-10T16:41"]}}`))
		})

	got, err := c.Current(context.Background(), "Санкт-Петербург")
	if err != nil {
		t.Fatal(err)
	}
	// Sunrise is 08:40 where the city is (UTC+3), whatever zone the server runs in.
	sunrise, sunset := got.Days[0].Sunrise, got.Days[0].Sunset
	if want := time.Date(2026, 11, 9, 5, 40, 0, 0, time.UTC); !sunrise.Equal(want) || sunrise.Format("15:04") != "08:40" {
		t.Fatalf("sunrise = %v, want 08:40 at UTC+3", sunrise)
	}
	if sunset.Sub(sunrise) != 8*time.Hour+3*time.Minute {
		t.Fatalf("daylight = %v", sunset.Sub(sunrise))
	}
	pct := 36
	want := Report{
		Location: "Санкт-Петербург", TemperatureC: 1.6, FeelsLikeC: -2.4, WindMS: 4.2, Code: 3, Description: "пасмурно", Icon: "☁️",
		UTCOffset: 3 * time.Hour,
		Days: []Day{
			{Date: "2026-11-09", MinC: 6.6, MaxC: 8.2, Code: 3, Description: "пасмурно", Icon: "☁️", PrecipitationPct: &pct, Sunrise: sunrise, Sunset: sunset},
			{Date: "2026-11-10", MinC: -3.3, MaxC: -0.5, Code: 71, Description: "снег", Icon: "🌨️", Sunrise: got.Days[1].Sunrise, Sunset: got.Days[1].Sunset},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if gotCity != "Санкт-Петербург" || gotQuery.Get("latitude") != "59.93863" {
		t.Fatalf("query params: city=%q lat=%q", gotCity, gotQuery.Get("latitude"))
	}
	// Without timezone=auto the days and the sun times would be in UTC.
	if gotQuery.Get("timezone") != "auto" || gotQuery.Get("forecast_days") != "5" || !strings.Contains(gotQuery.Get("daily"), "sunrise") {
		t.Fatalf("forecast query = %v", gotQuery)
	}
	if gotUA != fetch.UserAgent {
		t.Fatalf("User-Agent = %q", gotUA)
	}
}

func TestCurrentErrors(t *testing.T) {
	okGeo := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"name":"X","latitude":1,"longitude":2}]}`))
	}
	tests := []struct {
		name     string
		geocode  http.HandlerFunc
		forecast http.HandlerFunc
		want     string
	}{
		{"unknown city", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"generationtime_ms":0.1}`)) }, nil, "city not found"},
		{"geocode 500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, nil, "unexpected status 500"},
		{"forecast garbage", okGeo, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) }, "decode"},
		{"forecast without current", okGeo, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }, "no current block"},
		{"forecast without daily", okGeo, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"current":{}}`)) }, "no daily block"},
		{"daily arrays of different lengths", okGeo, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"current":{},"daily":{"time":["2026-11-09","2026-11-10"],"weather_code":[3],
				"temperature_2m_max":[1,2],"temperature_2m_min":[1,2],"precipitation_probability_max":[1,2],
				"sunrise":["",""],"sunset":["",""]}}`))
		}, "differ in length"},
		{"daily date garbage", okGeo, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"current":{},"daily":{"time":["tomorrow"],"weather_code":[3],
				"temperature_2m_max":[1],"temperature_2m_min":[1],"precipitation_probability_max":[1],
				"sunrise":[""],"sunset":[""]}}`))
		}, "daily date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := tt.forecast
			if fc == nil {
				fc = func(http.ResponseWriter, *http.Request) { t.Error("forecast must not be called") }
			}
			_, err := newTestClient(t, tt.geocode, fc).Current(context.Background(), "X")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	noForecast := func(http.ResponseWriter, *http.Request) { t.Error("forecast must not be called") }
	_, err := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }, noForecast).
		Current(context.Background(), "X")
	if !errors.Is(err, ErrCityNotFound) {
		t.Fatalf("ErrCityNotFound not wrapped: %v", err)
	}
}

func TestForecastDaysGapsAndPolarNight(t *testing.T) {
	okGeo := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"name":"Лонгйир","latitude":78.22,"longitude":15.65}]}`))
	}
	// Day 1: polar night, the API repeats midnight for both sun times.
	// Day 2: the model has no temperatures, so there is nothing to show.
	// Day 3: sun times that don't parse cost the day its sun line, not the forecast.
	c := newTestClient(t, okGeo, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"utc_offset_seconds":3600,"current":{},
			"daily":{"time":["2026-11-09","2026-11-10","2026-11-11"],"weather_code":[73,45,0],
				"temperature_2m_max":[-3.8,null,-9],"temperature_2m_min":[-12.9,null,-15],
				"precipitation_probability_max":[70,0,0],
				"sunrise":["2026-11-09T00:00","2026-11-10T00:00","soon"],
				"sunset":["2026-11-09T00:00","2026-11-10T00:00","2026-11-11T13:00"]}}`))
	})
	got, err := c.Current(context.Background(), "Лонгйир")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 2 || got.Days[0].Date != "2026-11-09" || got.Days[1].Date != "2026-11-11" {
		t.Fatalf("days = %+v, want the 9th and the 11th", got.Days)
	}
	for _, d := range got.Days {
		if !d.Sunrise.IsZero() || !d.Sunset.IsZero() {
			t.Errorf("%s: sun times %v–%v, want none", d.Date, d.Sunrise, d.Sunset)
		}
	}
	if p := got.Days[0].PrecipitationPct; p == nil || *p != 70 {
		t.Errorf("precipitation = %v, want 70", p)
	}
}

func TestDescribe(t *testing.T) {
	for code, want := range map[int]string{0: "ясно", 2: "малооблачно", 45: "туман", 63: "дождь", 86: "снег", 99: "гроза", 1234: ""} {
		if got, _ := Describe(code); got != want {
			t.Errorf("Describe(%d) = %q, want %q", code, got, want)
		}
	}
}
