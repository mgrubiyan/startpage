package httpapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/calendar"
)

// fakeCalendars serves parsed feeds by URL; a URL it doesn't know fails.
type fakeCalendars struct {
	mu    sync.Mutex
	feeds map[string]*calendar.Calendar
	urls  []string
}

func (f *fakeCalendars) Get(_ context.Context, url string) (cache.Result[*calendar.Calendar], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	c, ok := f.feeds[url]
	if !ok {
		return cache.Result[*calendar.Calendar]{}, errors.New("calendar example.com: unexpected status 404")
	}
	return cache.Result[*calendar.Calendar]{Value: c, FetchedAt: testNow}, nil
}

const (
	homeFeed = "https://p52-caldav.icloud.com/published/2/home-token"
	workFeed = "https://p52-caldav.icloud.com/published/2/work-token"
)

// testNow is Friday 6 November, 12:00 UTC: 15:00 in Moscow.
var moscowFeed = strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:morning
DTSTART;TZID=Europe/Moscow:20261106T100000
DTEND;TZID=Europe/Moscow:20261106T110000
SUMMARY:Утро
END:VEVENT
BEGIN:VEVENT
UID:now
DTSTART;TZID=Europe/Moscow:20261106T143000
DTEND;TZID=Europe/Moscow:20261106T153000
SUMMARY:Идёт сейчас
LOCATION:Офис
END:VEVENT
BEGIN:VEVENT
UID:evening
DTSTART;TZID=Europe/Moscow:20261106T180000
DTEND;TZID=Europe/Moscow:20261106T190000
SUMMARY:Вечер
END:VEVENT
BEGIN:VEVENT
UID:day
DTSTART;VALUE=DATE:20261106
DTEND;VALUE=DATE:20261107
SUMMARY:Весь день
END:VEVENT
BEGIN:VEVENT
UID:next-week
DTSTART;TZID=Europe/Moscow:20261113T100000
DTEND;TZID=Europe/Moscow:20261113T110000
SUMMARY:Через неделю
END:VEVENT
END:VCALENDAR
`, "\n", "\r\n")

func calendarEnv(t *testing.T, links ...string) (*testEnv, *fakeCalendars) {
	t.Helper()
	cal, err := calendar.Parse(strings.NewReader(moscowFeed))
	if err != nil {
		t.Fatal(err)
	}
	feeds := &fakeCalendars{feeds: map[string]*calendar.Calendar{homeFeed: cal}}
	env := newTestEnv(t, func(d *Deps) { d.Calendar = feeds })
	env.store.settings.Calendars = links
	return env, feeds
}

func TestCalendarEvents(t *testing.T) {
	env, feeds := calendarEnv(t, homeFeed, workFeed)
	got := decode[calendarDTO](t, env.do(t, "GET", "/api/calendar?tz=Europe/Moscow", ""))
	want := calendarDTO{
		Configured: true,
		From:       "2026-11-06",
		Events: []calendarEventDTO{
			{Title: "Весь день", Start: "2026-11-06", End: "2026-11-07", AllDay: true},
			{Title: "Идёт сейчас", Location: "Офис", Start: "2026-11-06T14:30:00+03:00", End: "2026-11-06T15:30:00+03:00"},
			{Title: "Вечер", Start: "2026-11-06T18:00:00+03:00", End: "2026-11-06T19:00:00+03:00"},
		},
		Failed: 1, // the work calendar
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if len(feeds.urls) != 2 {
		t.Errorf("loaded %v, want both calendars", feeds.urls)
	}
}

func TestCalendarDayFollowsViewerZone(t *testing.T) {
	env, _ := calendarEnv(t, homeFeed)
	// 12:00 UTC is 04:00 on 6 November in Los Angeles: the all-day event is in
	// the viewer's day, and the Moscow times come in the viewer's zone.
	got := decode[calendarDTO](t, env.do(t, "GET", "/api/calendar?tz=America/Los_Angeles", ""))
	if got.From != "2026-11-06" || len(got.Events) < 2 || got.Events[0].Title != "Весь день" {
		t.Fatalf("got %+v", got)
	}
	if got.Events[1].Start != "2026-11-06T03:30:00-08:00" {
		t.Errorf("start = %q, want it in the viewer's zone", got.Events[1].Start)
	}
}

func TestCalendarNotConfigured(t *testing.T) {
	env, feeds := calendarEnv(t)
	rec := env.do(t, "GET", "/api/calendar?tz=Europe/Moscow", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":false`) ||
		!strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if len(feeds.urls) != 0 {
		t.Errorf("fetched %v without links", feeds.urls)
	}
}

func TestCalendarAllFailed(t *testing.T) {
	env, _ := calendarEnv(t, workFeed)
	if rec := env.do(t, "GET", "/api/calendar?tz=Europe/Moscow", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d %s, want 502", rec.Code, rec.Body)
	}
}

func TestCalendarBadZone(t *testing.T) {
	env, _ := calendarEnv(t, homeFeed)
	for _, tz := range []string{"Mars/Olympus", "Local", "../../etc/passwd", strings.Repeat("A", 65)} {
		if rec := env.do(t, "GET", "/api/calendar?tz="+tz, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("tz=%q: got %d, want 400", tz, rec.Code)
		}
	}
}

func TestCalendarDemo(t *testing.T) {
	env := demoEnv(t)
	got := decode[calendarDTO](t, env.doAnon(t, "GET", "/api/calendar?tz=Europe/Moscow", ""))
	if !got.Configured || len(got.Events) == 0 {
		t.Fatalf("demo calendar = %+v", got)
	}
	// 15:00 in Moscow: the morning stand-up is over, the evening workout isn't.
	for _, e := range got.Events {
		if e.Title == "Планёрка" {
			t.Errorf("listed an event that is over: %+v", e)
		}
	}
	if got.Events[0].Title != "Тренировка" {
		t.Errorf("first event = %+v, want today's workout", got.Events[0])
	}
}

func TestSettingsCalendars(t *testing.T) {
	env, _ := calendarEnv(t)
	rec := env.do(t, "PUT", "/api/settings", `{"searchEngine":"g","city":"Москва","calendars":[
		" webcal://p52-caldav.icloud.com/published/2/home-token ",
		"https://p52-caldav.icloud.com/published/2/home-token",
		"webcal://p52-caldav.icloud.com/published/2/work-token"]}`)
	if got := decode[settingsDTO](t, rec); got.Calendars != 2 {
		t.Fatalf("calendars = %d, want 2 (one repeated)", got.Calendars)
	}
	// The links are read access to the calendars: they never come back.
	if strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("settings leak a link: %s", rec.Body)
	}
	if !reflect.DeepEqual(env.store.settings.Calendars, []string{homeFeed, workFeed}) {
		t.Fatalf("stored %q", env.store.settings.Calendars)
	}
	if body := env.do(t, "GET", "/api/settings", "").Body.String(); strings.Contains(body, "token") || !strings.Contains(body, `"calendars":2`) {
		t.Fatalf("GET settings = %s", body)
	}

	// Left out, as the page does when the field is empty: kept.
	decode[settingsDTO](t, env.do(t, "PUT", "/api/settings", `{"searchEngine":"y","city":"Москва"}`))
	if len(env.store.settings.Calendars) != 2 {
		t.Fatalf("links lost: %q", env.store.settings.Calendars)
	}
	// An empty list disconnects them.
	if got := decode[settingsDTO](t, env.do(t, "PUT", "/api/settings", `{"searchEngine":"y","city":"Москва","calendars":[]}`)); got.Calendars != 0 {
		t.Fatalf("calendars = %d after clearing", got.Calendars)
	}
}

func TestSettingsCalendarsRejected(t *testing.T) {
	env, _ := calendarEnv(t)
	for _, body := range []string{
		`{"searchEngine":"g","city":"Москва","calendars":["http://example.com/secret-feed.ics"]}`,
		`{"searchEngine":"g","city":"Москва","calendars":["not a link"]}`,
		`{"searchEngine":"g","city":"Москва","calendars":["webcal://a.example/1","webcal://a.example/2","webcal://a.example/3","webcal://a.example/4","webcal://a.example/5"]}`,
	} {
		rec := env.do(t, "PUT", "/api/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", body, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "secret-feed") {
			t.Errorf("error repeats the link: %s", rec.Body)
		}
	}
	if len(env.store.settings.Calendars) != 0 {
		t.Fatalf("stored %q after refusals", env.store.settings.Calendars)
	}
}
