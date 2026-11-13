package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// feed is shaped like an iCloud public calendar: CRLF lines, TZID times,
// overrides as separate VEVENTs with the series' UID.
var feed = strings.ReplaceAll(`BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//Apple Inc.//macOS 16.0//EN
X-WR-CALNAME:Дом
BEGIN:VEVENT
UID:single
DTSTART;TZID=Europe/Moscow:20261117T150000
DTEND;TZID=Europe/Moscow:20261117T163000
SUMMARY:Врач
LOCATION:Клиника на Невском
END:VEVENT
BEGIN:VEVENT
UID:standup
DTSTART;TZID=Europe/Moscow:20261102T100000
DTEND;TZID=Europe/Moscow:20261102T101500
RRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR
EXDATE;TZID=Europe/Moscow:20261116T100000,20261120T100000
SUMMARY:Стендап
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID;TZID=Europe/Moscow:20261118T100000
DTSTART;TZID=Europe/Moscow:20261118T120000
DTEND;TZID=Europe/Moscow:20261118T121500
SUMMARY:Стендап (перенесён)
END:VEVENT
BEGIN:VEVENT
UID:gym
DTSTART;TZID=Europe/Moscow:20261103T190000
DTEND;TZID=Europe/Moscow:20261103T203000
RRULE:FREQ=WEEKLY;BYDAY=TU,TH;UNTIL=20261231T000000Z
SUMMARY:Зал
END:VEVENT
BEGIN:VEVENT
UID:gym
RECURRENCE-ID;TZID=Europe/Moscow:20261119T190000
DTSTART;TZID=Europe/Moscow:20261119T190000
DTEND;TZID=Europe/Moscow:20261119T203000
STATUS:CANCELLED
SUMMARY:Зал
END:VEVENT
BEGIN:VEVENT
UID:trip
DTSTART;VALUE=DATE:20261114
DTEND;VALUE=DATE:20261117
SUMMARY:Поездка
END:VEVENT
BEGIN:VEVENT
UID:bday
DTSTART;VALUE=DATE:20001121
DTEND;VALUE=DATE:20001122
RRULE:FREQ=YEARLY
SUMMARY:День рождения
END:VEVENT
BEGIN:VEVENT
UID:club
DTSTART;TZID=Europe/Moscow:20260915T200000
DTEND;TZID=Europe/Moscow:20260915T220000
RRULE:FREQ=MONTHLY;BYDAY=3TU
SUMMARY:Книжный клуб
END:VEVENT
BEGIN:VEVENT
UID:floating
DTSTART:20261122T090000
DTEND:20261122T100000
SUMMARY:Пробежка
END:VEVENT
BEGIN:VEVENT
UID:past
DTSTART;TZID=Europe/Moscow:20261110T100000
DTEND;TZID=Europe/Moscow:20261110T110000
SUMMARY:Прошлое
END:VEVENT
BEGIN:VEVENT
UID:later
DTSTART;TZID=Europe/Moscow:20261123T100000
DTEND;TZID=Europe/Moscow:20261123T110000
SUMMARY:После окна
END:VEVENT
BEGIN:VEVENT
UID:exchange
DTSTART;TZID=Customized Time Zone:20261118T090000
DTEND;TZID=Customized Time Zone:20261118T100000
SUMMARY:Чужой пояс
END:VEVENT
BEGIN:VEVENT
UID:spam
DTSTART:20200101T000000Z
DTEND:20200101T000100Z
RRULE:FREQ=MINUTELY
SUMMARY:Каждую минуту
END:VEVENT
END:VCALENDAR
`, "\n", "\r\n")

func mustParse(t *testing.T, s string) *Calendar {
	t.Helper()
	c, err := Parse(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func describe(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		s := e.Start.Format("Mon 02 15:04") + " " + e.Title
		if e.AllDay {
			s = e.Start.Format("Mon 02") + " весь день " + e.Title + " до " + e.End.Format("Mon 02")
		}
		out = append(out, s)
	}
	return out
}

func TestBetween(t *testing.T) {
	msk := mustZone(t, "Europe/Moscow")
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, msk) // Monday
	events, skipped := mustParse(t, feed).Between(from, from.AddDate(0, 0, 7), msk)

	want := []string{
		"Sat 14 весь день Поездка до Tue 17",       // started before the window, still running
		"Tue 17 15:00 Врач",                        // TZID
		"Tue 17 19:00 Зал",                         // Thursday's is cancelled
		"Tue 17 20:00 Книжный клуб",                // third Tuesday of the month
		"Wed 18 12:00 Стендап (перенесён)",         // Monday and Friday excluded, Wednesday moved
		"Sat 21 весь день День рождения до Sun 22", // yearly, since 2000
		"Sun 22 09:00 Пробежка",                    // floating time, in the viewer's zone
	}
	if got := describe(events); !reflect.DeepEqual(got, want) {
		t.Errorf("events:\n got %q\nwant %q", got, want)
	}
	// The unknown TZID and the minutely rule.
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2", skipped)
	}
	if events[1].Location != "Клиника на Невском" {
		t.Errorf("location = %q", events[1].Location)
	}
}

func TestAllDayInViewerZone(t *testing.T) {
	ny := mustZone(t, "America/New_York")
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, ny)
	events, _ := mustParse(t, feed).Between(from, from.AddDate(0, 0, 7), ny)
	for _, e := range events {
		if e.Title == "День рождения" {
			if want := time.Date(2026, 11, 21, 0, 0, 0, 0, ny); !e.Start.Equal(want) {
				t.Errorf("birthday starts %v, want %v", e.Start, want)
			}
			return
		}
	}
	t.Fatal("birthday not listed")
}

func TestAllDaySeriesAcrossDST(t *testing.T) {
	berlin := mustZone(t, "Europe/Berlin")
	cal := mustParse(t, strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0",
		"BEGIN:VEVENT", "UID:sun",
		"DTSTART;VALUE=DATE:20261018", "DTEND;VALUE=DATE:20261019",
		"RRULE:FREQ=WEEKLY", "SUMMARY:Воскресенье",
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n"))
	// Clocks go back on 25 October: that Sunday has 25 hours.
	from := time.Date(2026, 10, 25, 0, 0, 0, 0, berlin)
	events, _ := cal.Between(from, from.AddDate(0, 0, 1), berlin)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if want := time.Date(2026, 10, 26, 0, 0, 0, 0, berlin); !events[0].End.Equal(want) {
		t.Errorf("ends %v, want midnight %v", events[0].End, want)
	}
}

func TestSeriesCapped(t *testing.T) {
	cal := mustParse(t, strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0",
		"BEGIN:VEVENT", "UID:hourly",
		"DTSTART:20260101T000000Z", "DTEND:20260101T001000Z",
		"RRULE:FREQ=HOURLY", "SUMMARY:Каждый час",
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n"))
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	events, _ := cal.Between(from, from.AddDate(0, 0, 7), time.UTC)
	if len(events) != maxPerSeries {
		t.Errorf("got %d occurrences, want the cap %d", len(events), maxPerSeries)
	}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"webcal://p52-caldav.icloud.com/published/2/TOKEN", "https://p52-caldav.icloud.com/published/2/TOKEN", true},
		{"  WEBCAL://p52-caldav.icloud.com/published/2/TOKEN  ", "https://p52-caldav.icloud.com/published/2/TOKEN", true},
		{"https://calendar.google.com/calendar/ical/x/private-y/basic.ics#frag", "https://calendar.google.com/calendar/ical/x/private-y/basic.ics", true},
		{"http://example.com/cal.ics", "", false}, // the token would travel in clear text
		{"https://user:pass@example.com/cal.ics", "", false},
		{"javascript:alert(1)", "", false},
		{"webcal:///no-host", "", false},
		{"", "", false},
		{"https://example.com/" + strings.Repeat("a", 2048), "", false},
	}
	for _, tt := range tests {
		got, err := ParseURL(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("ParseURL(%q) = %q, %v; want %q, ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestLoad(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/published/secret-token":
			w.Header().Set("Content-Type", "text/calendar")
			fmt.Fprint(w, feed)
		case "/huge":
			fmt.Fprint(w, strings.Repeat("x", MaxFeedBytes+1))
		case "/garbage":
			fmt.Fprint(w, "<html>not a calendar</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := NewFetcher(srv.Client())
	ctx := context.Background()

	cal, err := f.Load(ctx, srv.URL+"/published/secret-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.events) == 0 {
		t.Fatal("no events parsed")
	}

	for _, path := range []string{"/gone-secret-token", "/huge", "/garbage"} {
		_, err := f.Load(ctx, srv.URL+path)
		if err == nil {
			t.Errorf("%s: want an error", path)
			continue
		}
		// The path carries the token: it must not reach the logs.
		if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), path) {
			t.Errorf("%s: error leaks the path: %v", path, err)
		}
	}

	// Connection errors come wrapped in *url.Error, which prints the URL.
	srv.Close()
	if _, err := f.Load(ctx, srv.URL+"/published/secret-token"); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("closed server: got %v, want an error without the path", err)
	}
}

func TestRedirectsStayOnHTTPS(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/cal.ics", nil)
	check := NewClient().CheckRedirect
	if err := check(req, []*http.Request{req}); err == nil {
		t.Error("redirect to http allowed")
	}
	secure := httptest.NewRequest(http.MethodGet, "https://example.com/cal.ics", nil)
	if err := check(secure, []*http.Request{req}); err != nil {
		t.Errorf("redirect to https refused: %v", err)
	}
	if err := check(secure, make([]*http.Request, 5)); err == nil {
		t.Error("sixth redirect allowed")
	}
}
