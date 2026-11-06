package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/weather"
)

// WeatherSource returns current weather for a city; *cache.Cache[weather.Report] implements it.
type WeatherSource interface {
	Get(ctx context.Context, city string) (cache.Result[weather.Report], error)
}

// RatesSource returns exchange rates; the key is ignored. *cache.Cache[rates.Rates] implements it.
type RatesSource interface {
	Get(ctx context.Context, key string) (cache.Result[rates.Rates], error)
}

// SuggestSource returns search suggestions for a query; *cache.Cache[[]string] implements it.
type SuggestSource interface {
	Get(ctx context.Context, query string) (cache.Result[[]string], error)
}

const maxSuggestQueryRunes = 100

type weatherDTO struct {
	Location    string    `json:"location"`
	Temperature float64   `json:"temperature"`
	FeelsLike   float64   `json:"feelsLike"`
	WindMS      float64   `json:"windMs"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	FetchedAt   time.Time `json:"fetchedAt"`
	Stale       bool      `json:"stale"`
}

type rateDTO struct {
	Value    float64 `json:"value"`
	Previous float64 `json:"previous"`
}

type ratesDTO struct {
	Date      time.Time `json:"date"`
	USD       rateDTO   `json:"usd"`
	EUR       rateDTO   `json:"eur"`
	FetchedAt time.Time `json:"fetchedAt"`
	Stale     bool      `json:"stale"`
}

type suggestDTO struct {
	Query       string   `json:"query"`
	Suggestions []string `json:"suggestions"`
}

func (a *api) getWeather(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.Settings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	res, err := a.weather.Get(r.Context(), settings.City)
	switch {
	case errors.Is(err, weather.ErrCityNotFound):
		writeError(w, http.StatusNotFound, "city not found")
		return
	case err != nil:
		a.upstreamError(w, r, "weather", err)
		return
	}
	rep := res.Value
	writeJSON(w, http.StatusOK, weatherDTO{
		Location:    rep.Location,
		Temperature: rep.TemperatureC,
		FeelsLike:   rep.FeelsLikeC,
		WindMS:      rep.WindMS,
		Description: rep.Description,
		Icon:        rep.Icon,
		FetchedAt:   res.FetchedAt,
		Stale:       res.Stale,
	})
}

func (a *api) getRates(w http.ResponseWriter, r *http.Request) {
	res, err := a.rates.Get(r.Context(), "")
	if err != nil {
		a.upstreamError(w, r, "rates", err)
		return
	}
	v := res.Value
	writeJSON(w, http.StatusOK, ratesDTO{
		Date:      v.Date,
		USD:       rateDTO(v.USD),
		EUR:       rateDTO(v.EUR),
		FetchedAt: res.FetchedAt,
		Stale:     res.Stale,
	})
}

func (a *api) getSuggestions(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, suggestDTO{Query: "", Suggestions: []string{}})
		return
	}
	if !utf8.ValidString(q) || utf8.RuneCountInString(q) > maxSuggestQueryRunes {
		writeError(w, http.StatusBadRequest, "q must be valid UTF-8, at most 100 characters")
		return
	}
	// Case-folded key: "Погода" and "погода" share one cache entry and one upstream call.
	res, err := a.suggest.Get(r.Context(), strings.ToLower(q))
	if err != nil {
		a.upstreamError(w, r, "suggest", err)
		return
	}
	writeJSON(w, http.StatusOK, suggestDTO{Query: q, Suggestions: res.Value})
}

func (a *api) upstreamError(w http.ResponseWriter, r *http.Request, what string, err error) {
	if r.Context().Err() != nil {
		return // client went away; nobody is listening
	}
	a.logger.WarnContext(r.Context(), "upstream failed", "upstream", what, "err", err)
	writeError(w, http.StatusBadGateway, what+" unavailable")
}
