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
	// Ping reports whether the service can reach its database.
	Ping func(ctx context.Context) error
}

// New builds the root handler.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz(d.Ping))
	mux.Handle("GET /", http.FileServerFS(d.Static))

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
