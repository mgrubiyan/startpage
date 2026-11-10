// Package httpapi exposes the startpage JSON API and serves the embedded frontend.
package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Deps are the collaborators the HTTP layer needs. Everything is an interface
// so handlers can be tested with in-memory fakes.
type Deps struct {
	Logger  *slog.Logger
	Static  fs.FS
	Store   Store
	Weather WeatherSource
	Rates   RatesSource
	Suggest SuggestSource
	Art     ArtSource
	// Crypto and Stocks are the market boards; either may be nil to leave it out.
	Crypto MarketSource
	Stocks MarketSource
	Auth   Authenticator
	// LoginLimiter throttles POST /api/login per client IP.
	LoginLimiter RateLimiter
	// SecureCookie sets the Secure flag on the session cookie (enable behind HTTPS).
	SecureCookie bool
	// TrustProxy honours the last X-Forwarded-For entry as the client IP.
	TrustProxy bool
	// Ping reports whether the service can reach its database.
	Ping func(ctx context.Context) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// OnServicesChanged is called after a service is added or removed; may be nil.
	OnServicesChanged func()
	// Demo serves a public read-only showcase: reads need no session, writes
	// (including login) get 403. Auth may be nil in this mode.
	Demo bool
	// SuggestLimiter, when set, throttles GET /api/suggest per client IP. A
	// public demo needs it so it can't be used as a free suggestions proxy.
	SuggestLimiter RateLimiter
}

type route struct {
	pattern string
	handler http.HandlerFunc
	public  bool // reachable without a session
}

// routes is the single source of truth for the API surface; tests walk it to
// prove that every non-public route is behind requireAuth.
func (a *api) routes() []route {
	return []route{
		{"POST /api/login", a.login, true},
		{"POST /api/logout", a.logout, true},
		{"GET /api/meta", a.getMeta, true},

		{"GET /api/settings", a.getSettings, false},
		{"PUT /api/settings", a.putSettings, false},
		{"GET /api/bookmarks", a.listBookmarks, false},
		{"POST /api/bookmarks", a.createBookmark, false},
		{"DELETE /api/bookmarks/{id}", a.deleteBookmark, false},
		{"GET /api/note", a.getNote, false},
		{"PUT /api/note", a.putNote, false},
		{"GET /api/tasks", a.listTasks, false},
		{"POST /api/tasks", a.createTask, false},
		{"PATCH /api/tasks/{id}", a.patchTask, false},
		{"DELETE /api/tasks/{id}", a.deleteTask, false},
		// A literal segment beats {id} in the mux, so "completed" never reaches deleteTask.
		{"DELETE /api/tasks/completed", a.deleteDoneTasks, false},
		{"GET /api/services", a.listServices, false},
		{"POST /api/services", a.createService, false},
		{"DELETE /api/services/{id}", a.deleteService, false},
		{"GET /api/weather", a.getWeather, false},
		{"GET /api/rates", a.getRates, false},
		{"GET /api/suggest", a.getSuggestions, false},
		{"GET /api/markets", a.getMarkets, false},
		{"GET /api/art", a.getArt, false},
		{"GET /api/art/image", a.getArtImage, false},
	}
}

// New builds the root handler.
func New(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	a := &api{
		store:             d.Store,
		weather:           d.Weather,
		rates:             d.Rates,
		suggest:           d.Suggest,
		art:               d.Art,
		crypto:            d.Crypto,
		stocks:            d.Stocks,
		auth:              d.Auth,
		limiter:           d.LoginLimiter,
		secureCookie:      d.SecureCookie,
		trustProxy:        d.TrustProxy,
		logger:            d.Logger,
		now:               d.Now,
		onServicesChanged: d.OnServicesChanged,
		demo:              d.Demo,
		suggestLimiter:    d.SuggestLimiter,
	}

	apiMux := http.NewServeMux()
	for _, rt := range a.routes() {
		// Per route rather than around the mux, so an unknown path is a 404,
		// not a 415 for the wrong content type.
		apiMux.Handle(rt.pattern, requireJSON(a.guard(rt)))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(d.Ping))
	mux.Handle("/api/", chain(jsonMuxErrors(apiMux), noStore))
	// "/" rather than "GET /": the latter conflicts with the method-less "/api/" pattern.
	mux.Handle("/", readOnly(http.FileServerFS(d.Static)))

	// logRequests sits outside recoverPanics so a panicking request still gets
	// its access-log line (with the 500 that recoverPanics writes).
	return chain(mux,
		logRequests(d.Logger),
		recoverPanics(d.Logger),
		securityHeaders,
	)
}

// guard applies the access policy to a route. Normally every non-public route
// needs a session. In demo mode reads are open to everyone and every write is
// refused, so visitors can't change the data or add their own probe targets.
func (a *api) guard(rt route) http.HandlerFunc {
	method, _, _ := strings.Cut(rt.pattern, " ")
	switch {
	case a.demo && method != http.MethodGet:
		return demoReadOnly
	case a.demo, rt.public:
		return rt.handler
	default:
		return a.requireAuth(rt.handler)
	}
}

func demoReadOnly(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusForbidden, "demo mode is read-only")
}

type metaDTO struct {
	Demo bool `json:"demo"`
}

// getMeta tells the frontend how the server runs, e.g. to hide edit controls in demo mode.
func (a *api) getMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, metaDTO{Demo: a.demo})
}

func healthz(ping func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "db unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
