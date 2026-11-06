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
	Logger *slog.Logger
	Static fs.FS
	Store  Store
	// Ping reports whether the service can reach its database.
	Ping func(ctx context.Context) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// OnServicesChanged is called after a service is added or removed; may be nil.
	OnServicesChanged func()
}

// New builds the root handler.
func New(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	a := &api{store: d.Store, logger: d.Logger, now: d.Now, onServicesChanged: d.OnServicesChanged}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/settings", a.getSettings)
	apiMux.HandleFunc("PUT /api/settings", a.putSettings)
	apiMux.HandleFunc("GET /api/bookmarks", a.listBookmarks)
	apiMux.HandleFunc("POST /api/bookmarks", a.createBookmark)
	apiMux.HandleFunc("DELETE /api/bookmarks/{id}", a.deleteBookmark)
	apiMux.HandleFunc("GET /api/services", a.listServices)
	apiMux.HandleFunc("POST /api/services", a.createService)
	apiMux.HandleFunc("DELETE /api/services/{id}", a.deleteService)

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
