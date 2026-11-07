package httpapi

import (
	"log/slog"
	"mime"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type middleware func(http.Handler) http.Handler

func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func logRequests(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

func recoverPanics(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler { //nolint:errorlint // net/http compares this sentinel by identity too
						panic(v)
					}
					logger.Error("panic in handler", "panic", v, "path", r.URL.Path, "stack", string(debug.Stack()))
					writeError(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// contentSecurityPolicy allows only same-origin scripts and API calls.
// Favicons come from Google's favicon service, which may redirect to gstatic.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; " +
	"img-src 'self' data: https://www.google.com https://*.gstatic.com; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// noStore keeps API responses (bookmarks, service URLs, sessions) out of browser
// and proxy caches; static assets stay cacheable.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// jsonMuxErrors makes the mux's own 404/405 responses JSON like every other API error,
// while keeping its routing decision (including the Allow header on 405).
func jsonMuxErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r) // sets r.PathValue
			return
		}
		capture := &statusCapture{header: http.Header{}}
		h.ServeHTTP(capture, r)
		if allow := capture.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		status := capture.status
		if status == 0 {
			status = http.StatusNotFound
		}
		writeError(w, status, strings.ToLower(http.StatusText(status)))
	})
}

// statusCapture records headers and status and discards the body.
type statusCapture struct {
	header http.Header
	status int
}

func (c *statusCapture) Header() http.Header { return c.header }

func (c *statusCapture) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	return len(b), nil
}

func (c *statusCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

// readOnly allows only GET and HEAD, for static content.
func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireJSON rejects body-carrying state changes that are not application/json.
// Together with SameSite=Lax cookies this blocks cross-site form posts (CSRF):
// a browser cannot send a cross-origin JSON request without a CORS preflight we never approve.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
