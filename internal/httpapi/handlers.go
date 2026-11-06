package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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
	ServiceStatuses(ctx context.Context, since time.Time) ([]store.ServiceStatus, error)
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
}

type api struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
	// onServicesChanged lets the monitor check a new service right away instead of waiting a tick.
	onServicesChanged func()
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
		writeError(w, http.StatusBadRequest, err.Error())
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
		writeError(w, http.StatusBadRequest, err.Error())
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
	statuses, err := a.store.ServiceStatuses(r.Context(), a.now().Add(-UptimeWindow))
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]serviceDTO, 0, len(statuses))
	for _, st := range statuses {
		dto := serviceDTO{ID: st.ID, Name: st.Name, URL: st.URL}
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
		writeError(w, http.StatusBadRequest, err.Error())
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
	err := a.store.DeleteService(r.Context(), id)
	if err == nil {
		a.servicesChanged()
	}
	a.deleted(w, r, err)
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
