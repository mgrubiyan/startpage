package weather

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	var gotCity, gotLat, gotUA string
	c := newTestClient(t,
		func(w http.ResponseWriter, r *http.Request) {
			gotCity, gotUA = r.URL.Query().Get("name"), r.UserAgent()
			_, _ = w.Write([]byte(`{"results":[{"name":"Санкт-Петербург","latitude":59.93863,"longitude":30.31413}]}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			gotLat = r.URL.Query().Get("latitude")
			_, _ = w.Write([]byte(`{"current":{"temperature_2m":1.6,"apparent_temperature":-2.4,"weather_code":3,"wind_speed_10m":4.2}}`))
		})

	got, err := c.Current(context.Background(), "Санкт-Петербург")
	if err != nil {
		t.Fatal(err)
	}
	want := Report{Location: "Санкт-Петербург", TemperatureC: 1.6, FeelsLikeC: -2.4, WindMS: 4.2, Code: 3, Description: "пасмурно", Icon: "☁️"}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if gotCity != "Санкт-Петербург" || gotLat != "59.93863" {
		t.Fatalf("query params: city=%q lat=%q", gotCity, gotLat)
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

func TestDescribe(t *testing.T) {
	for code, want := range map[int]string{0: "ясно", 2: "малооблачно", 45: "туман", 63: "дождь", 86: "снег", 99: "гроза", 1234: ""} {
		if got, _ := Describe(code); got != want {
			t.Errorf("Describe(%d) = %q, want %q", code, got, want)
		}
	}
}
