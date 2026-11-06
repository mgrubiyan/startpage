package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEveryPrivateRouteRequiresSession walks the route table, so a route added
// without auth fails here instead of shipping open.
func TestEveryPrivateRouteRequiresSession(t *testing.T) {
	env := newTestEnv(t)
	var private, public int
	for _, rt := range (&api{}).routes() {
		if rt.public {
			public++
			continue
		}
		private++
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = strings.ReplaceAll(path, "{id}", "1")
		body := ""
		if method == http.MethodPost || method == http.MethodPut {
			body = "{}"
		}
		t.Run(rt.pattern, func(t *testing.T) {
			if rec := env.doAnon(t, method, path, body); rec.Code != http.StatusUnauthorized {
				t.Fatalf("no cookie: status %d, want 401", rec.Code)
			}
			bad := env.send(t, method, path, body, &http.Cookie{Name: sessionCookie, Value: "forged"})
			if bad.Code != http.StatusUnauthorized {
				t.Fatalf("forged cookie: status %d, want 401", bad.Code)
			}
		})
	}
	if private < 10 || public != 2 {
		t.Fatalf("route table changed shape: %d private, %d public", private, public)
	}
	if n := len(env.store.bookmarks); n != 1 {
		t.Fatalf("anonymous requests changed the store: %d bookmarks", n)
	}
}

func TestLoginLogoutFlow(t *testing.T) {
	env := newTestEnv(t)

	if rec := env.doAnon(t, "POST", "/api/login", `{"password":"nope"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", rec.Code)
	}

	rec := env.doAnon(t, "POST", "/api/login", `{"password":"correct-horse"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %v", cookies)
	}
	c := cookies[0]
	if c.Name != sessionCookie || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Secure || c.MaxAge != 3600 {
		t.Fatalf("session cookie attributes wrong: %+v", c)
	}

	if got := env.send(t, "GET", "/api/settings", "", c); got.Code != http.StatusOK {
		t.Fatalf("authenticated request = %d", got.Code)
	}

	out := env.send(t, "POST", "/api/logout", "{}", c)
	if out.Code != http.StatusNoContent {
		t.Fatalf("logout = %d", out.Code)
	}
	if cleared := out.Result().Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout did not clear cookie: %+v", cleared)
	}
	if got := env.send(t, "GET", "/api/settings", "", c); got.Code != http.StatusUnauthorized {
		t.Fatalf("old cookie after logout = %d", got.Code)
	}
}

func TestAPIResponsesAreNotCached(t *testing.T) {
	env := newTestEnv(t)
	for _, rec := range []*httptest.ResponseRecorder{
		env.do(t, "GET", "/api/bookmarks", ""),
		env.doAnon(t, "GET", "/api/settings", ""),        // 401 too
		env.doAnon(t, "PATCH", "/api/settings", "{}"),    // mux 405
		env.doAnon(t, "POST", "/api/login", "password="), // 415 from requireJSON
	} {
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("status %d: Cache-Control = %q, want no-store", rec.Code, got)
		}
	}
	if got := env.doAnon(t, "GET", "/healthz", "").Header().Get("Cache-Control"); got == "no-store" {
		t.Error("no-store leaked outside /api/")
	}
}

func TestSecureCookieFlag(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.SecureCookie = true })
	rec := env.doAnon(t, "POST", "/api/login", `{"password":"correct-horse"}`)
	if c := rec.Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Fatalf("Secure flag missing: %+v", c)
	}
}

func TestLoginRateLimited(t *testing.T) {
	env := newTestEnv(t)
	for i := range 5 {
		if rec := env.doAnon(t, "POST", "/api/login", `{"password":"guess"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d", i+1, rec.Code)
		}
	}
	rec := env.doAnon(t, "POST", "/api/login", `{"password":"correct-horse"}`)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("6th attempt = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trustProxy bool
		remote     string
		xff        string
		want       string
	}{
		{"direct", false, "203.0.113.7:51234", "", "203.0.113.7"},
		{"xff ignored without trust", false, "203.0.113.7:51234", "1.1.1.1", "203.0.113.7"},
		{"trusted proxy, single hop", true, "172.18.0.2:4000", "198.51.100.9", "198.51.100.9"},
		{"trusted proxy, forged prefix", true, "172.18.0.2:4000", "1.1.1.1, 198.51.100.9", "198.51.100.9"},
		{"trusted proxy, no header", true, "172.18.0.2:4000", "", "172.18.0.2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &api{trustProxy: tt.trustProxy}
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := a.clientIP(r); got != tt.want {
				t.Fatalf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}
