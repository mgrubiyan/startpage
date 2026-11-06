package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mgrubiyan/startpage/internal/auth"
)

// Authenticator manages password login and sessions; *auth.Service implements it.
type Authenticator interface {
	Login(ctx context.Context, password string) (token string, expires time.Time, err error)
	Authenticate(ctx context.Context, token string) (bool, error)
	Logout(ctx context.Context, token string) error
}

// RateLimiter throttles login attempts per client; *auth.Limiter implements it.
type RateLimiter interface {
	Allow(key string) bool
}

const sessionCookie = "startpage_session"

type loginInput struct {
	Password string `json:"password"`
}

// requireAuth rejects requests without a valid session cookie.
func (a *api) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ok, err := a.auth.Authenticate(r.Context(), c.Value)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (a *api) login(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
		return
	}
	var in loginInput
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	token, expires, err := a.auth.Login(r.Context(), in.Password)
	if errors.Is(err, auth.ErrInvalidPassword) {
		writeError(w, http.StatusUnauthorized, "invalid password")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	http.SetCookie(w, a.cookie(token, int(expires.Sub(a.now()).Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.auth.Logout(r.Context(), c.Value); err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	http.SetCookie(w, a.cookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// cookie builds the session cookie; maxAge < 0 deletes it.
func (a *api) cookie(value string, maxAge int) *http.Cookie {
	// Secure comes from STARTPAGE_SECURE_COOKIE: it must be off for plain-HTTP localhost.
	return &http.Cookie{ //nolint:gosec // G124: Secure is config-driven, HttpOnly and SameSite are set
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.secureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

// clientIP returns the address used for login rate limiting.
//
// X-Forwarded-For is client-controlled unless a trusted proxy overwrites it, so it is
// only honoured with TrustProxy, and then only its last entry: the one appended by
// our own reverse proxy. Earlier entries could be forged to dodge the limit.
func (a *api) clientIP(r *http.Request) string {
	if a.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				return last
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
