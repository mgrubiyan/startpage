// Package httpapi exposes the startpage JSON API and serves the embedded frontend.
package httpapi

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
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
	Auth    Authenticator
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

		{"GET /api/settings", a.getSettings, false},
		{"PUT /api/settings", a.putSettings, false},
		{"GET /api/bookmarks", a.listBookmarks, false},
		{"POST /api/bookmarks", a.createBookmark, false},
		{"DELETE /api/bookmarks/{id}", a.deleteBookmark, false},
		{"GET /api/services", a.listServices, false},
		{"POST /api/services", a.createService, false},
		{"DELETE /api/services/{id}", a.deleteService, false},
		{"GET /api/weather", a.getWeather, false},
		{"GET /api/rates", a.getRates, false},
		{"GET /api/suggest", a.getSuggestions, false},
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
		auth:              d.Auth,
		limiter:           d.LoginLimiter,
		secureCookie:      d.SecureCookie,
		trustProxy:        d.TrustProxy,
		logger:            d.Logger,
		now:               d.Now,
		onServicesChanged: d.OnServicesChanged,
	}

	apiMux := http.NewServeMux()
	for _, rt := range a.routes() {
		h := rt.handler
		if !rt.public {
			h = a.requireAuth(h)
		}
		apiMux.HandleFunc(rt.pattern, h)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz(d.Ping))
	mux.Handle("/api/", chain(jsonMuxErrors(apiMux), requireJSON))
	// "/" rather than "GET /": the latter conflicts with the method-less "/api/" pattern.
	mux.Handle("/", readOnly(http.FileServerFS(d.Static)))

	return chain(mux,
		recoverPanics(d.Logger),
		logRequests(d.Logger),
		securityHeaders,
	)
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
