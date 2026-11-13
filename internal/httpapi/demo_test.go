package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/auth"
	"github.com/mgrubiyan/startpage/internal/store"
)

func demoEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnv(t, func(d *Deps) {
		d.Demo = true
		d.Auth = nil // demo mode must never reach the authenticator
	})
}

// TestDemoModeIsReadOnly walks the route table: every read is open without a
// session and every write, login included, is refused without touching the store.
func TestDemoModeIsReadOnly(t *testing.T) {
	env := demoEnv(t)
	// One existing service, so a leaked DELETE /api/services/1 would have something to remove.
	env.store.statuses = []store.ServiceStatus{{Service: store.Service{ID: 1, Name: "bot", URL: "https://bot.example"}}}
	// And one task and a note, so leaked task writes or a note save would show.
	env.store.tasks = []store.Task{{ID: 1, Title: "demo", Done: true}}
	env.store.note = store.Note{Text: "demo note"}
	before := env.store.settings
	var reads, writes int
	for _, rt := range (&api{}).routes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = strings.ReplaceAll(path, "{id}", "1")
		if method == http.MethodGet {
			reads++
		} else {
			writes++
		}
		t.Run(rt.pattern, func(t *testing.T) {
			if method == http.MethodGet {
				if rec := env.doAnon(t, method, path, ""); rec.Code != http.StatusOK {
					t.Fatalf("anonymous read = %d %s, want 200", rec.Code, rec.Body)
				}
				return
			}
			// A body every write handler would accept, so a 403 can only come from the guard.
			body := `{"password":"correct-horse","title":"x","url":"https://evil.example","name":"x","searchEngine":"y","city":"Москва","text":"x","done":false}`
			rec := env.doAnon(t, method, path, body)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("write = %d, want 403", rec.Code)
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatal("demo write set a cookie")
			}
		})
	}
	if reads < 6 || writes < 7 {
		t.Fatalf("route table changed shape: %d reads, %d writes", reads, writes)
	}
	if len(env.store.bookmarks) != 1 || !reflect.DeepEqual(env.store.settings, before) ||
		len(env.store.statuses) != 1 || env.servicesChange != 0 ||
		len(env.store.tasks) != 1 || !env.store.tasks[0].Done || env.store.note.Text != "demo note" {
		t.Fatalf("demo writes changed state: %d bookmarks, settings %+v, %d services, %d service changes",
			len(env.store.bookmarks), env.store.settings, len(env.store.statuses), env.servicesChange)
	}
}

func TestMetaReportsMode(t *testing.T) {
	if got := decode[metaDTO](t, newTestEnv(t).doAnon(t, "GET", "/api/meta", "")); got.Demo {
		t.Fatal("normal mode reported demo")
	}
	if got := decode[metaDTO](t, demoEnv(t).doAnon(t, "GET", "/api/meta", "")); !got.Demo {
		t.Fatal("demo mode not reported")
	}
}

func TestCleanURLChecksEncodedLength(t *testing.T) {
	// 1000 Cyrillic letters: 2000 bytes raw, so under the limit, but about 6000
	// characters once percent-encoded, which the database would reject.
	long := "https://example.com/" + strings.Repeat("я", 1000)
	if _, err := cleanURL(long); err == nil {
		t.Fatal("URL over the limit once encoded was accepted")
	}
	env := newTestEnv(t)
	if rec := env.do(t, "POST", "/api/bookmarks", `{"title":"long","url":"`+long+`"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("POST long bookmark = %d %s, want 400", rec.Code, rec.Body)
	}
	// A short non-ASCII URL is still accepted, in encoded form.
	if got, err := cleanURL("https://example.com/привет"); err != nil || !strings.HasPrefix(got, "https://example.com/%D0%BF") {
		t.Fatalf("cleanURL = %q, %v", got, err)
	}
}

func TestSuggestRateLimited(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) { d.SuggestLimiter = auth.NewLimiter(2, time.Minute) })
	for i := range 2 {
		if rec := env.do(t, "GET", "/api/suggest?q=go", ""); rec.Code != http.StatusOK {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	rec := env.do(t, "GET", "/api/suggest?q=go", "")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over limit = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if len(env.suggest.keys) != 2 {
		t.Fatalf("throttled request reached upstream: %d calls", len(env.suggest.keys))
	}
}
