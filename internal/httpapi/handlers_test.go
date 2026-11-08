package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/mgrubiyan/startpage/internal/auth"
	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/store"
	"github.com/mgrubiyan/startpage/internal/weather"
)

var testNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// fakeStore is an in-memory Store. failWith makes every call return that error.
type fakeStore struct {
	mu        sync.Mutex
	settings  store.Settings
	bookmarks []store.Bookmark
	statuses  []store.ServiceStatus
	note      store.Note
	tasks     []store.Task
	nextID    int64
	since     time.Time
	failWith  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings:  store.Settings{SearchEngine: "g", City: "Санкт-Петербург"},
		bookmarks: []store.Bookmark{{ID: 1, Title: "GitHub", URL: "https://github.com"}},
		nextID:    100,
	}
}

func (f *fakeStore) Settings(context.Context) (store.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.settings, f.failWith
}

func (f *fakeStore) UpdateSettings(_ context.Context, s store.Settings) (store.Settings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Settings{}, f.failWith
	}
	f.settings = s
	return s, nil
}

func (f *fakeStore) Bookmarks(context.Context) ([]store.Bookmark, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Bookmark(nil), f.bookmarks...), f.failWith
}

func (f *fakeStore) AddBookmark(_ context.Context, b store.Bookmark) (store.Bookmark, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Bookmark{}, f.failWith
	}
	f.nextID++
	b.ID = f.nextID
	f.bookmarks = append(f.bookmarks, b)
	return b, nil
}

func (f *fakeStore) DeleteBookmark(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	for i, b := range f.bookmarks {
		if b.ID == id {
			f.bookmarks = append(f.bookmarks[:i], f.bookmarks[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) Note(context.Context) (store.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.note, f.failWith
}

func (f *fakeStore) SaveNote(_ context.Context, text string) (store.Note, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Note{}, f.failWith
	}
	f.note = store.Note{Text: text, UpdatedAt: testNow}
	return f.note, nil
}

func (f *fakeStore) Tasks(context.Context) ([]store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Task(nil), f.tasks...), f.failWith
}

func (f *fakeStore) AddTask(_ context.Context, title string) (store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Task{}, f.failWith
	}
	f.nextID++
	t := store.Task{ID: f.nextID, Title: title}
	f.tasks = append(f.tasks, t)
	return t, nil
}

func (f *fakeStore) SetTaskDone(_ context.Context, id int64, done bool) (store.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Task{}, f.failWith
	}
	for i := range f.tasks {
		if f.tasks[i].ID == id {
			f.tasks[i].Done = done
			return f.tasks[i], nil
		}
	}
	return store.Task{}, store.ErrNotFound
}

func (f *fakeStore) DeleteTask(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	for i, t := range f.tasks {
		if t.ID == id {
			f.tasks = append(f.tasks[:i], f.tasks[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) DeleteDoneTasks(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return 0, f.failWith
	}
	kept := f.tasks[:0]
	for _, t := range f.tasks {
		if !t.Done {
			kept = append(kept, t)
		}
	}
	n := int64(len(f.tasks) - len(kept))
	f.tasks = kept
	return n, nil
}

func (f *fakeStore) ServiceStatuses(_ context.Context, since time.Time) ([]store.ServiceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = since
	return f.statuses, f.failWith
}

func (f *fakeStore) AddService(_ context.Context, s store.Service) (store.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return store.Service{}, f.failWith
	}
	f.nextID++
	s.ID = f.nextID
	f.statuses = append(f.statuses, store.ServiceStatus{Service: s})
	return s, nil
}

func (f *fakeStore) DeleteService(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	for i, s := range f.statuses {
		if s.ID == id {
			f.statuses = append(f.statuses[:i], f.statuses[i+1:]...)
			return nil
		}
	}
	return store.ErrNotFound
}

// fakeSource is an in-memory stand-in for a *cache.Cache that records requested keys.
type fakeSource[V any] struct {
	mu    sync.Mutex
	value V
	stale bool
	err   error
	keys  []string
}

func (f *fakeSource[V]) Get(_ context.Context, key string) (cache.Result[V], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	if f.err != nil {
		return cache.Result[V]{}, f.err
	}
	return cache.Result[V]{Value: f.value, FetchedAt: testNow, Stale: f.stale}, nil
}

const testToken = "valid-test-session"

// fakeAuth accepts password "correct-horse" and issues sequential tokens.
type fakeAuth struct {
	mu        sync.Mutex
	sessions  map[string]bool
	issued    int
	logoutErr error
}

func (f *fakeAuth) Login(_ context.Context, password string) (string, time.Time, error) {
	if password != "correct-horse" {
		return "", time.Time{}, auth.ErrInvalidPassword
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued++
	tok := "issued-" + strconv.Itoa(f.issued)
	f.sessions[tok] = true
	return tok, testNow.Add(time.Hour), nil
}

func (f *fakeAuth) Authenticate(_ context.Context, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessions[token], nil
}

func (f *fakeAuth) Logout(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logoutErr != nil {
		return f.logoutErr
	}
	delete(f.sessions, token)
	return nil
}

type testEnv struct {
	handler        http.Handler
	auth           *fakeAuth
	store          *fakeStore
	weather        *fakeSource[weather.Report]
	rates          *fakeSource[rates.Rates]
	suggest        *fakeSource[[]string]
	art            *fakeArt
	servicesChange int
	pingErr        error
}

func newTestEnv(t *testing.T, opts ...func(*Deps)) *testEnv {
	t.Helper()
	env := &testEnv{
		auth:    &fakeAuth{sessions: map[string]bool{testToken: true}},
		store:   newFakeStore(),
		weather: &fakeSource[weather.Report]{},
		rates:   &fakeSource[rates.Rates]{},
		suggest: &fakeSource[[]string]{value: []string{}},
		art:     &fakeArt{artwork: testArtwork},
	}
	d := Deps{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Static:            fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>t</title>")}},
		Store:             env.store,
		Weather:           env.weather,
		Rates:             env.rates,
		Suggest:           env.suggest,
		Art:               env.art,
		Auth:              env.auth,
		LoginLimiter:      auth.NewLimiter(5, time.Minute),
		Ping:              func(context.Context) error { return env.pingErr },
		Now:               func() time.Time { return testNow },
		OnServicesChanged: func() { env.servicesChange++ },
	}
	for _, opt := range opts {
		opt(&d)
	}
	env.handler = New(d)
	return env
}

// do sends an authenticated request.
func (e *testEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.send(t, method, path, body, &http.Cookie{Name: sessionCookie, Value: testToken})
}

// doAnon sends a request without a session cookie.
func (e *testEnv) doAnon(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.send(t, method, path, body, nil)
}

// A cross-site form can't send application/json; real routes refuse anything
// else (CSRF defence), while unknown paths still answer 404 rather than 415.
func TestFormPostsRejected(t *testing.T) {
	env := newTestEnv(t)
	for _, tt := range []struct {
		path string
		want int
	}{
		{"/api/bookmarks", http.StatusUnsupportedMediaType},
		{"/api/logout", http.StatusUnsupportedMediaType},
		{"/api/nope", http.StatusNotFound},
	} {
		req := httptest.NewRequest("POST", tt.path, strings.NewReader("title=x&url=https://evil.example"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: testToken})
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		if rec.Code != tt.want {
			t.Errorf("POST %s as form = %d, want %d", tt.path, rec.Code, tt.want)
		}
	}
	if n := len(env.store.bookmarks); n != 1 {
		t.Fatalf("form post changed the store: %d bookmarks", n)
	}
}

func (e *testEnv) send(t *testing.T, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestValidationAndStatusCodes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantErr    string
	}{
		{"list bookmarks", "GET", "/api/bookmarks", "", 200, ""},
		{"bookmark ok", "POST", "/api/bookmarks", `{"title":"Go","url":"https://go.dev"}`, 201, ""},
		{"bookmark javascript url", "POST", "/api/bookmarks", `{"title":"x","url":"javascript:alert(1)"}`, 400, "url:"},
		{"bookmark relative url", "POST", "/api/bookmarks", `{"title":"x","url":"/etc/passwd"}`, 400, "url:"},
		{"bookmark empty title", "POST", "/api/bookmarks", `{"title":"   ","url":"https://go.dev"}`, 400, "title:"},
		{"bookmark title 41 runes", "POST", "/api/bookmarks", `{"title":"` + strings.Repeat("я", 41) + `","url":"https://go.dev"}`, 400, "at most 40"},
		{"bookmark title 40 runes", "POST", "/api/bookmarks", `{"title":"` + strings.Repeat("я", 40) + `","url":"https://go.dev"}`, 201, ""},
		{"bookmark control char", "POST", "/api/bookmarks", `{"title":"a\u0000b","url":"https://go.dev"}`, 400, "control"},
		{"unknown field", "POST", "/api/bookmarks", `{"title":"x","url":"https://go.dev","admin":true}`, 400, "unknown field"},
		{"trailing data", "POST", "/api/bookmarks", `{"title":"x","url":"https://go.dev"}{}`, 400, "unexpected data"},
		{"trailing brace", "POST", "/api/bookmarks", `{"title":"x","url":"https://go.dev"}}`, 400, "invalid json"},
		{"trailing whitespace ok", "POST", "/api/bookmarks", "{\"title\":\"x\",\"url\":\"https://go.dev\"}\n ", 201, ""},
		{"oversized body", "POST", "/api/bookmarks", `{"title":"` + strings.Repeat("a", 70<<10) + `"}`, 413, "body larger"},
		{"malformed json", "POST", "/api/bookmarks", `{"title":`, 400, "invalid json"},
		{"delete bad id", "DELETE", "/api/bookmarks/abc", "", 400, "positive integer"},
		{"delete zero id", "DELETE", "/api/bookmarks/0", "", 400, "positive integer"},
		{"delete missing", "DELETE", "/api/bookmarks/999", "", 404, "not found"},
		{"delete existing", "DELETE", "/api/bookmarks/1", "", 204, ""},
		{"settings ok", "PUT", "/api/settings", `{"searchEngine":"y","city":" Москва "}`, 200, ""},
		{"settings unknown engine", "PUT", "/api/settings", `{"searchEngine":"bing","city":"Москва"}`, 400, "searchEngine"},
		{"settings empty city", "PUT", "/api/settings", `{"searchEngine":"g","city":""}`, 400, "city:"},
		{"service ok", "POST", "/api/services", `{"name":"bot","url":"http://instatrack:8080/health"}`, 201, ""},
		{"service ftp", "POST", "/api/services", `{"name":"bot","url":"ftp://x"}`, 400, "url:"},
		{"wrong method", "PATCH", "/api/bookmarks", `{}`, 405, ""},
		{"unknown api route", "GET", "/api/nope", "", 404, "not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			rec := env.do(t, tt.method, tt.path, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantErr != "" && !strings.Contains(rec.Body.String(), tt.wantErr) {
				t.Fatalf("body %s does not mention %q", rec.Body, tt.wantErr)
			}
		})
	}
}

func TestMutationsRequireJSONContentType(t *testing.T) {
	env := newTestEnv(t)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		req := httptest.NewRequest("POST", "/api/bookmarks", strings.NewReader(`{"title":"x","url":"https://go.dev"}`))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: testToken})
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status %d, want 415", ct, rec.Code)
		}
	}
	if n := len(env.store.bookmarks); n != 1 {
		t.Fatalf("store changed by rejected requests: %d bookmarks", n)
	}
}

func TestCreateBookmarkPersistsCleanedValues(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, "POST", "/api/bookmarks", `{"title":"  Go  ","url":" https://go.dev/doc "}`)
	got := decode[bookmarkDTO](t, rec)
	if got.Title != "Go" || got.URL != "https://go.dev/doc" || got.ID == 0 {
		t.Fatalf("created = %+v", got)
	}
	list := decode[[]bookmarkDTO](t, env.do(t, "GET", "/api/bookmarks", ""))
	if len(list) != 2 || list[1] != got {
		t.Fatalf("list = %+v", list)
	}
}

func TestServicesReportUptimeAndNotifyMonitor(t *testing.T) {
	env := newTestEnv(t)
	env.store.statuses = []store.ServiceStatus{
		{
			Service:     store.Service{ID: 7, Name: "bot", URL: "http://bot/health"},
			Last:        &store.Check{OK: false, Latency: 1500 * time.Millisecond, Error: "timeout", CheckedAt: testNow},
			TotalChecks: 4, OKChecks: 3,
		},
		{Service: store.Service{ID: 8, Name: "new", URL: "http://new/health"}},
	}

	list := decode[[]serviceDTO](t, env.do(t, "GET", "/api/services", ""))
	if !env.store.since.Equal(testNow.Add(-UptimeWindow)) {
		t.Fatalf("uptime window since = %v", env.store.since)
	}
	if list[0].Uptime == nil || *list[0].Uptime != 75 || list[0].LastCheck == nil ||
		list[0].LastCheck.LatencyMs != 1500 || list[0].LastCheck.Error != "timeout" {
		t.Fatalf("checked service = %+v / %+v", list[0], list[0].LastCheck)
	}
	if list[1].Uptime != nil || list[1].LastCheck != nil {
		t.Fatalf("unchecked service must have null uptime and lastCheck: %+v", list[1])
	}

	env.do(t, "POST", "/api/services", `{"name":"x","url":"https://x.dev"}`)
	if env.servicesChange != 1 {
		t.Fatalf("adding a service notified the monitor %d times, want 1", env.servicesChange)
	}
	// Deleting must not wake the monitor: an early round would re-probe every other service.
	if rec := env.do(t, "DELETE", "/api/services/8", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if env.servicesChange != 1 {
		t.Fatalf("deleting a service notified the monitor (%d notifications)", env.servicesChange)
	}
}

func TestStoreErrorsDoNotLeak(t *testing.T) {
	env := newTestEnv(t)
	env.store.failWith = errors.New("pq: password authentication failed for user secret")
	for _, path := range []string{"/api/bookmarks", "/api/settings", "/api/services"} {
		rec := env.do(t, "GET", path, "")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret") || !strings.Contains(rec.Body.String(), "internal error") {
			t.Fatalf("%s leaks or hides error: %s", path, rec.Body)
		}
	}
}

func TestHealthzAndStatic(t *testing.T) {
	env := newTestEnv(t)
	if rec := env.do(t, "GET", "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	env.pingErr = errors.New("down")
	if rec := env.do(t, "GET", "/healthz", ""); rec.Code != 503 {
		t.Fatalf("healthz with db down = %d", rec.Code)
	}
	rec := env.do(t, "GET", "/", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>t</title>") {
		t.Fatalf("index = %d %s", rec.Code, rec.Body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("CSP missing: %q", csp)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		recoverPanics(slog.New(slog.NewTextHandler(io.Discard, nil))))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
}
