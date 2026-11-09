package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/weather"
)

func TestWeatherUsesCityFromSettings(t *testing.T) {
	env := newTestEnv(t)
	env.store.settings.City = "Москва"
	env.weather.value = weather.Report{Location: "Москва", TemperatureC: 1.5, FeelsLikeC: -3, WindMS: 3, Description: "снег", Icon: "🌨️"}
	env.weather.stale = true

	got := decode[weatherDTO](t, env.do(t, "GET", "/api/weather", ""))
	if env.weather.keys[0] != "Москва" {
		t.Fatalf("weather requested for %q", env.weather.keys)
	}
	if got.Location != "Москва" || got.Temperature != 1.5 || got.FeelsLike != -3 || got.Icon != "🌨️" || !got.Stale || !got.FetchedAt.Equal(testNow) {
		t.Fatalf("weather = %+v", got)
	}
}

func TestWeatherForecast(t *testing.T) {
	env := newTestEnv(t)
	msk := time.FixedZone("", 3*60*60)
	pct := 40
	env.weather.value = weather.Report{
		Location:  "Москва",
		UTCOffset: 3 * time.Hour,
		Days: []weather.Day{
			// Over in Moscow by testNow (noon UTC on the 6th): a stale report still has it.
			{Date: "2026-11-05", MinC: 1, MaxC: 2},
			{
				Date: "2026-11-06", MinC: -2.5, MaxC: 1, Description: "снег", Icon: "🌨️", PrecipitationPct: &pct,
				Sunrise: time.Date(2026, 11, 6, 7, 48, 0, 0, msk), Sunset: time.Date(2026, 11, 6, 16, 41, 0, 0, msk),
			},
			{Date: "2026-11-07", MinC: -4, MaxC: -1, Description: "ясно", Icon: "☀️"}, // no sun times
		},
	}

	rec := env.do(t, "GET", "/api/weather", "")
	got := decode[weatherDTO](t, rec)
	want := []forecastDayDTO{
		{Date: "2026-11-06", Min: -2.5, Max: 1, Description: "снег", Icon: "🌨️", Precipitation: &pct, Sunrise: "07:48", Sunset: "16:41", DaylightMinutes: 533},
		{Date: "2026-11-07", Min: -4, Max: -1, Description: "ясно", Icon: "☀️"},
	}
	if !reflect.DeepEqual(got.Days, want) {
		t.Fatalf("days = %+v\nwant %+v", got.Days, want)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"precipitation":null`) || strings.Count(body, `"sunrise"`) != 1 {
		t.Fatalf("an unknown chance must be null and missing sun times left out: %s", body)
	}
}

// The day changes at the city's midnight, not at the server's or at UTC's.
func TestWeatherForecastUsesCityCalendar(t *testing.T) {
	env := newTestEnv(t)
	days := []weather.Day{{Date: "2026-11-06"}, {Date: "2026-11-07"}}
	for _, tt := range []struct {
		offset time.Duration
		first  string
	}{
		{-11 * time.Hour, "2026-11-06"}, // 01:00 on the 6th
		{13 * time.Hour, "2026-11-07"},  // 01:00 on the 7th already
	} {
		env.weather.value = weather.Report{UTCOffset: tt.offset, Days: days}
		got := decode[weatherDTO](t, env.do(t, "GET", "/api/weather", ""))
		if len(got.Days) == 0 || got.Days[0].Date != tt.first {
			t.Errorf("offset %v: days = %+v, want them to start on %s", tt.offset, got.Days, tt.first)
		}
	}
}

func TestWeatherWithoutForecastSendsEmptyList(t *testing.T) {
	env := newTestEnv(t)
	if body := env.do(t, "GET", "/api/weather", "").Body.String(); !strings.Contains(body, `"days":[]`) {
		t.Fatalf("days must be [], not null: %s", body)
	}
}

func TestWeatherErrors(t *testing.T) {
	env := newTestEnv(t)
	env.weather.err = fmt.Errorf("geocode: %w", weather.ErrCityNotFound)
	if rec := env.do(t, "GET", "/api/weather", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown city = %d", rec.Code)
	}
	env.weather.err = errors.New("dial tcp: i/o timeout")
	rec := env.do(t, "GET", "/api/weather", "")
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "dial") {
		t.Fatalf("upstream failure = %d %s", rec.Code, rec.Body)
	}
}

func TestRates(t *testing.T) {
	env := newTestEnv(t)
	env.rates.value = rates.Rates{USD: rates.Rate{Value: 82.14, Previous: 81.9}, EUR: rates.Rate{Value: 92.19, Previous: 92.5}}
	got := decode[ratesDTO](t, env.do(t, "GET", "/api/rates", ""))
	if got.USD != (rateDTO{82.14, 81.9}) || got.EUR != (rateDTO{92.19, 92.5}) {
		t.Fatalf("rates = %+v", got)
	}
	env.rates.err = errors.New("down")
	if rec := env.do(t, "GET", "/api/rates", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("rates down = %d", rec.Code)
	}
}

func TestSuggest(t *testing.T) {
	env := newTestEnv(t)
	env.suggest.value = []string{"погода спб", "погода завтра"}

	got := decode[suggestDTO](t, env.do(t, "GET", "/api/suggest?q=+%D0%9F%D0%BE%D0%B3%D0%BE%D0%B4%D0%B0+", ""))
	if got.Query != "Погода" || len(got.Suggestions) != 2 {
		t.Fatalf("suggest = %+v", got)
	}
	if env.suggest.keys[0] != "погода" {
		t.Fatalf("cache key %q must be trimmed and lower-cased", env.suggest.keys[0])
	}

	empty := decode[suggestDTO](t, env.do(t, "GET", "/api/suggest?q=%20%20", ""))
	if empty.Suggestions == nil || len(empty.Suggestions) != 0 || len(env.suggest.keys) != 1 {
		t.Fatalf("blank query must short-circuit with []: %+v, upstream keys %v", empty, env.suggest.keys)
	}

	long := strings.Repeat("я", maxSuggestQueryRunes+1)
	if rec := env.do(t, "GET", "/api/suggest?q="+long, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("overlong query = %d", rec.Code)
	}

	env.suggest.err = errors.New("429")
	if rec := env.do(t, "GET", "/api/suggest?q=x", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("upstream failure = %d", rec.Code)
	}
}
