package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Store is the persistence the API needs; *postgres.Store implements it.
type Store interface {
	Settings(ctx context.Context) (store.Settings, error)
	UpdateSettings(ctx context.Context, s store.Settings) (store.Settings, error)
	Bookmarks(ctx context.Context) ([]store.Bookmark, error)
	AddBookmark(ctx context.Context, b store.Bookmark) (store.Bookmark, error)
	DeleteBookmark(ctx context.Context, id int64) error
	Note(ctx context.Context) (store.Note, error)
	SaveNote(ctx context.Context, text string) (store.Note, error)
	Tasks(ctx context.Context) ([]store.Task, error)
	AddTask(ctx context.Context, title string) (store.Task, error)
	SetTaskDone(ctx context.Context, id int64, done bool) (store.Task, error)
	DeleteTask(ctx context.Context, id int64) error
	DeleteDoneTasks(ctx context.Context) (int64, error)
	ServiceStatuses(ctx context.Context, since time.Time) ([]store.ServiceStatus, error)
	CheckHistory(ctx context.Context, since time.Time, slot time.Duration) (map[int64][]store.CheckBucket, error)
	AddService(ctx context.Context, s store.Service) (store.Service, error)
	DeleteService(ctx context.Context, id int64) error
}

// UptimeWindow is the period over which service uptime is reported.
const UptimeWindow = 24 * time.Hour

// searchEngines are the keys the frontend knows how to turn into search URLs.
var searchEngines = map[string]bool{"g": true, "y": true, "d": true, "gh": true, "yt": true, "w": true}

type settingsDTO struct {
	SearchEngine string `json:"searchEngine"`
	City         string `json:"city"`
}

type bookmarkDTO struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type bookmarkInput struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type serviceInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type checkDTO struct {
	OK         bool      `json:"ok"`
	StatusCode int       `json:"statusCode,omitempty"`
	LatencyMs  int64     `json:"latencyMs"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
}

type serviceDTO struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	LastCheck *checkDTO `json:"lastCheck"`
	Uptime    *float64  `json:"uptime"` // percent over UptimeWindow; null until checked
	// History is HistorySlots hourly slots, oldest first, the last one being
	// the current, unfinished hour. A slot without checks has total 0.
	History []slotDTO `json:"history"`
}

type slotDTO struct {
	Start     time.Time `json:"start"`
	Total     int       `json:"total"`
	OK        int       `json:"ok"`
	LatencyMs int64     `json:"latencyMs"` // average over successful checks; 0 if none
}

// A service's history is HistorySlots slots of historySlot each: one day by the hour.
const (
	HistorySlots = 24
	historySlot  = time.Hour
)

// historyStart is where the history row begins: the start of the slot now is
// in, HistorySlots-1 slots back. Both the store query and the layout use it.
func historyStart(now time.Time) time.Time {
	return now.UTC().Truncate(historySlot).Add(-(HistorySlots - 1) * historySlot)
}

// historySlots lays a service's non-empty buckets onto a dense row of slots
// from first, so the page draws it as is.
func historySlots(buckets []store.CheckBucket, first time.Time) []slotDTO {
	out := make([]slotDTO, HistorySlots)
	for i := range out {
		out[i].Start = first.Add(time.Duration(i) * historySlot)
	}
	for _, b := range buckets {
		i := int(b.Start.Sub(first) / historySlot)
		if i < 0 || i >= HistorySlots || !b.Start.Equal(out[i].Start) {
			continue // outside the window or not on a slot boundary
		}
		out[i].Total, out[i].OK, out[i].LatencyMs = b.Total, b.OK, b.AvgLatency.Milliseconds()
	}
	return out
}

type api struct {
	store   Store
	weather WeatherSource
	rates   RatesSource
	suggest SuggestSource
	art     ArtSource
	crypto  MarketSource
	stocks  MarketSource
	auth    Authenticator
	limiter RateLimiter
	logger  *slog.Logger

	secureCookie bool
	trustProxy   bool
	now          func() time.Time
	// onServicesChanged lets the monitor check a new service right away instead of waiting a tick.
	onServicesChanged func()

	demo           bool
	suggestLimiter RateLimiter // nil means unlimited
}

func (a *api) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.ErrorContext(r.Context(), "request failed", "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ---------- settings ----------

func (a *api) getSettings(w http.ResponseWriter, r *http.Request) {
	s, err := a.store.Settings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO(s))
}

func (a *api) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsDTO
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	if !searchEngines[in.SearchEngine] {
		writeError(w, http.StatusBadRequest, "searchEngine: unknown engine")
		return
	}
	city, err := cleanText(in.City, 60)
	if err != nil {
		writeError(w, http.StatusBadRequest, "city: "+err.Error())
		return
	}
	saved, err := a.store.UpdateSettings(r.Context(), store.Settings{SearchEngine: in.SearchEngine, City: city})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO(saved))
}

// ---------- bookmarks ----------

func (a *api) listBookmarks(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Bookmarks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]bookmarkDTO, 0, len(list))
	for _, b := range list {
		out = append(out, bookmarkDTO(b))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) createBookmark(w http.ResponseWriter, r *http.Request) {
	var in bookmarkInput
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	title, err := cleanText(in.Title, 40)
	if err != nil {
		writeError(w, http.StatusBadRequest, "title: "+err.Error())
		return
	}
	u, err := cleanURL(in.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "url: "+err.Error())
		return
	}
	b, err := a.store.AddBookmark(r.Context(), store.Bookmark{Title: title, URL: u})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, bookmarkDTO(b))
}

func (a *api) deleteBookmark(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	a.deleted(w, r, a.store.DeleteBookmark(r.Context(), id))
}

// ---------- services ----------

func (a *api) listServices(w http.ResponseWriter, r *http.Request) {
	// One instant for the whole response, so the uptime and the bars agree on the hour.
	now := a.now()
	first := historyStart(now)
	var (
		wg         sync.WaitGroup
		history    map[int64][]store.CheckBucket
		historyErr error
	)
	// The page polls this every 30s; the two queries needn't wait for each other.
	wg.Go(func() { history, historyErr = a.store.CheckHistory(r.Context(), first, historySlot) })
	statuses, err := a.store.ServiceStatuses(r.Context(), now.Add(-UptimeWindow))
	wg.Wait()
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// The bars are a nice-to-have: without them the list still shows status and uptime.
	if historyErr != nil && r.Context().Err() == nil {
		a.logger.WarnContext(r.Context(), "check history failed", "err", historyErr)
	}
	out := make([]serviceDTO, 0, len(statuses))
	for _, st := range statuses {
		dto := serviceDTO{ID: st.ID, Name: st.Name, URL: st.URL, History: []slotDTO{}}
		if historyErr == nil {
			dto.History = historySlots(history[st.ID], first)
		}
		if st.Last != nil {
			dto.LastCheck = &checkDTO{
				OK:         st.Last.OK,
				StatusCode: st.Last.StatusCode,
				LatencyMs:  st.Last.Latency.Milliseconds(),
				Error:      st.Last.Error,
				CheckedAt:  st.Last.CheckedAt,
			}
		}
		if up, ok := st.Uptime(); ok {
			dto.Uptime = &up
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) createService(w http.ResponseWriter, r *http.Request) {
	var in serviceInput
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	name, err := cleanText(in.Name, 60)
	if err != nil {
		writeError(w, http.StatusBadRequest, "name: "+err.Error())
		return
	}
	u, err := cleanURL(in.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "url: "+err.Error())
		return
	}
	svc, err := a.store.AddService(r.Context(), store.Service{Name: name, URL: u})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.servicesChanged()
	writeJSON(w, http.StatusCreated, serviceDTO{ID: svc.ID, Name: svc.Name, URL: svc.URL})
}

func (a *api) deleteService(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	// No monitor trigger: the next round simply won't find the service, while
	// an early round would re-probe every remaining service for nothing.
	a.deleted(w, r, a.store.DeleteService(r.Context(), id))
}

func (a *api) servicesChanged() {
	if a.onServicesChanged != nil {
		a.onServicesChanged()
	}
}

func (a *api) deleted(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		a.internalError(w, r, err)
	}
}
