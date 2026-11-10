package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mgrubiyan/startpage/internal/art"
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
	Location    string  `json:"location"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feelsLike"`
	WindMS      float64 `json:"windMs"`
	Description string  `json:"description"`
	Icon        string  `json:"icon"`
	// Days is the forecast from today on, in the city's own calendar.
	Days      []forecastDayDTO `json:"days"`
	FetchedAt time.Time        `json:"fetchedAt"`
	Stale     bool             `json:"stale"`
}

// forecastDayDTO is one day of the forecast. Sunrise and sunset are wall-clock
// "HH:MM" in the city, so the page needn't know its time zone; they are absent
// on a day the sun doesn't rise or set.
type forecastDayDTO struct {
	Date            string  `json:"date"` // YYYY-MM-DD
	Min             float64 `json:"min"`
	Max             float64 `json:"max"`
	Description     string  `json:"description"`
	Icon            string  `json:"icon"`
	Precipitation   *int    `json:"precipitation"` // chance in percent; null if unknown
	Sunrise         string  `json:"sunrise,omitempty"`
	Sunset          string  `json:"sunset,omitempty"`
	DaylightMinutes int     `json:"daylightMinutes,omitempty"`
}

// forecastDays drops days that are already over in the city: a report served
// stale after an upstream outage may begin with yesterday.
func forecastDays(rep weather.Report, now time.Time) []forecastDayDTO {
	today := now.UTC().Add(rep.UTCOffset).Format(time.DateOnly)
	out := make([]forecastDayDTO, 0, len(rep.Days))
	for _, d := range rep.Days {
		if d.Date < today {
			continue
		}
		dto := forecastDayDTO{
			Date:          d.Date,
			Min:           d.MinC,
			Max:           d.MaxC,
			Description:   d.Description,
			Icon:          d.Icon,
			Precipitation: d.PrecipitationPct,
		}
		if !d.Sunrise.IsZero() && !d.Sunset.IsZero() {
			dto.Sunrise = d.Sunrise.Format("15:04")
			dto.Sunset = d.Sunset.Format("15:04")
			dto.DaylightMinutes = int(d.Sunset.Sub(d.Sunrise).Minutes())
		}
		out = append(out, dto)
	}
	return out
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
		Days:        forecastDays(rep, a.now()),
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
	if a.suggestLimiter != nil && !a.suggestLimiter.Allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many requests, try again later")
		return
	}
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

// ArtSource returns the painting of the day; *art.Daily implements it.
type ArtSource interface {
	Today(ctx context.Context) (art.Artwork, error)
}

type artDTO struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Date   string `json:"date"`
	URL    string `json:"url"`
	// Image changes with the painting, so the browser may cache it for the day.
	Image string `json:"image"`
}

func (a *api) getArt(w http.ResponseWriter, r *http.Request) {
	aw, ok := a.artwork(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, artDTO{
		Title:  aw.Title,
		Artist: aw.Artist,
		Date:   aw.Date,
		URL:    aw.URL,
		Image:  "/api/art/image?v=" + strconv.Itoa(aw.ID),
	})
}

func (a *api) getArtImage(w http.ResponseWriter, r *http.Request) {
	aw, ok := a.artwork(w, r)
	if !ok {
		return
	}
	switch v := r.URL.Query().Get("v"); v {
	case strconv.Itoa(aw.ID):
		// The URL /api/art handed out for this painting: cacheable, but only by
		// the browser, since on a personal instance the image is behind the login.
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	case "":
		// No version asked for: today's image, uncached (no-store from the middleware).
	default:
		// The painting changed since the page asked for it. Serving today's image
		// here would put it under yesterday's caption; the page keeps what it has.
		writeError(w, http.StatusNotFound, "painting changed")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(aw.Image)))
	// Several MB can take longer than the server's 20s WriteTimeout on a slow link.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Minute))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(aw.Image)
}

func (a *api) artwork(w http.ResponseWriter, r *http.Request) (art.Artwork, bool) {
	// Before the first painting has loaded, Today waits for the download, which
	// can outlast the server's 20s WriteTimeout. (Unsupported writers just skip this.)
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(3 * time.Minute))
	aw, err := a.art.Today(r.Context())
	switch {
	case errors.Is(err, art.ErrUnavailable):
		// The museum failed moments ago and that was logged; the page just stays plain.
		writeError(w, http.StatusServiceUnavailable, "art unavailable")
		return art.Artwork{}, false
	case err != nil:
		a.upstreamError(w, r, "art", err)
		return art.Artwork{}, false
	}
	return aw, true
}
