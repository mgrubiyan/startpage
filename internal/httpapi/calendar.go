package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/calendar"
)

// CalendarSource returns the parsed feed at a URL.
// *cache.Cache[*calendar.Calendar] implements it.
type CalendarSource interface {
	Get(ctx context.Context, url string) (cache.Result[*calendar.Calendar], error)
}

const (
	maxCalendars      = 4  // matches the migration's CHECK
	calendarDays      = 7  // today and the six days after it
	maxCalendarEvents = 40 // a week of a busy calendar; more wouldn't fit the panel anyway
)

type calendarEventDTO struct {
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	// RFC 3339 instants; for all-day events plain dates (YYYY-MM-DD), End
	// exclusive, so they don't shift a day when shown in another zone.
	Start  string `json:"start"`
	End    string `json:"end"`
	AllDay bool   `json:"allDay"`
}

type calendarDTO struct {
	// Configured is false until a calendar link is saved; the page hides the panel.
	Configured bool `json:"configured"`
	// From is the first day shown (today in the requested zone), so the page
	// labels days the way the server cut them.
	From   string             `json:"from"`
	Events []calendarEventDTO `json:"events"`
	// Failed counts calendars that couldn't be loaded, while others could.
	Failed int `json:"failed,omitempty"`
	// Skipped counts events that couldn't be read, e.g. in an unknown time zone.
	Skipped int  `json:"skipped,omitempty"`
	Stale   bool `json:"stale,omitempty"`
}

// getCalendar lists the events from now to the end of the week's last day.
// The query parameter tz is the browser's IANA zone: it decides where a day
// begins and where all-day and floating events fall, and the server runs in
// another zone than its user.
func (a *api) getCalendar(w http.ResponseWriter, r *http.Request) {
	loc, err := zone(r.URL.Query().Get("tz"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "tz: "+err.Error())
		return
	}
	now := a.now().In(loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, calendarDays)
	out := calendarDTO{From: from.Format(time.DateOnly), Events: []calendarEventDTO{}}

	var events []calendar.Event
	if a.demo {
		// No outbound request: the demo shows a made-up week.
		out.Configured = true
		events = demoEvents(from)
	} else {
		s, err := a.store.Settings(r.Context())
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		if len(s.Calendars) == 0 || a.calendars == nil {
			writeJSON(w, http.StatusOK, out)
			return
		}
		out.Configured = true
		results := a.loadCalendars(r, s.Calendars)
		if r.Context().Err() != nil {
			return // client went away
		}
		for _, res := range results {
			if res.err != nil {
				out.Failed++
				continue
			}
			evs, skipped := res.Value.Between(from, to, loc)
			events = append(events, evs...)
			out.Skipped += skipped
			out.Stale = out.Stale || res.Stale
		}
		if out.Failed == len(results) {
			writeError(w, http.StatusBadGateway, "calendar unavailable")
			return
		}
		calendar.Sort(events)
	}

	for _, e := range events {
		if ended(e, now) {
			continue
		}
		out.Events = append(out.Events, toEventDTO(e, loc))
		if len(out.Events) == maxCalendarEvents {
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type calendarResult struct {
	cache.Result[*calendar.Calendar]
	err error
}

// loadCalendars fetches every feed at once; a slow one shouldn't hold up the rest.
func (a *api) loadCalendars(r *http.Request, urls []string) []calendarResult {
	out := make([]calendarResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Go(func() {
			res, err := a.calendars.Get(r.Context(), u)
			// The error names the host only: the link itself is a credential.
			if err != nil && r.Context().Err() == nil {
				a.logger.WarnContext(r.Context(), "upstream failed", "upstream", "calendar", "n", i+1, "err", err)
			}
			out[i] = calendarResult{Result: res, err: err}
		})
	}
	wg.Wait()
	return out
}

// ended drops what is over already: the panel looks ahead.
func ended(e calendar.Event, now time.Time) bool {
	if e.End.After(e.Start) {
		return !e.End.After(now)
	}
	return e.Start.Before(now)
}

// toEventDTO writes times in the viewer's zone, whatever zone the feed used.
func toEventDTO(e calendar.Event, loc *time.Location) calendarEventDTO {
	dto := calendarEventDTO{Title: e.Title, Location: e.Location, AllDay: e.AllDay}
	if dto.Title == "" {
		dto.Title = "Без названия"
	}
	if e.AllDay {
		dto.Start, dto.End = e.Start.Format(time.DateOnly), e.End.Format(time.DateOnly)
	} else {
		dto.Start, dto.End = e.Start.In(loc).Format(time.RFC3339), e.End.In(loc).Format(time.RFC3339)
	}
	return dto
}

// zone resolves the tz parameter. Empty means UTC, for a hand-made request.
func zone(name string) (*time.Location, error) {
	if len(name) > 64 || name == "Local" {
		return nil, errors.New("unknown time zone")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errors.New("unknown time zone")
	}
	return loc, nil
}

// cleanCalendars checks the feed links from the settings form and drops
// repeats. Errors name the link by its place in the list, not by itself: it
// is a secret, and error messages get logged.
func cleanCalendars(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for i, raw := range in {
		u, err := calendar.ParseURL(raw)
		if err != nil {
			return nil, fmt.Errorf("link %d: %w", i+1, err)
		}
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	if len(out) > maxCalendars {
		return nil, fmt.Errorf("at most %d calendars", maxCalendars)
	}
	return out, nil
}

// demoEvents is a plausible week for the public demo, laid out from the
// first day shown. Events of today that are over drop out as on a real page.
func demoEvents(day time.Time) []calendar.Event {
	timed := func(title, place string, d, h, m int, length time.Duration) calendar.Event {
		s := time.Date(day.Year(), day.Month(), day.Day()+d, h, m, 0, 0, day.Location())
		return calendar.Event{Title: title, Location: place, Start: s, End: s.Add(length)}
	}
	allDay := func(title string, d int) calendar.Event {
		s := day.AddDate(0, 0, d)
		return calendar.Event{Title: title, Start: s, End: s.AddDate(0, 0, 1), AllDay: true}
	}
	events := []calendar.Event{
		timed("Планёрка", "", 0, 9, 30, 15*time.Minute),
		timed("Обед с командой", "Кафе на углу", 0, 13, 0, time.Hour),
		timed("Тренировка", "Спортзал", 0, 19, 0, 90*time.Minute),
		allDay("Сдать отчёт", 1),
		timed("Созвон с заказчиком", "", 1, 11, 0, time.Hour),
		timed("Стоматолог", "Клиника на Садовой", 2, 16, 30, 45*time.Minute),
		timed("Кино", "Аврора", 3, 19, 30, 2*time.Hour+30*time.Minute),
		allDay("День рождения Саши", 5),
		timed("Поход в Эрмитаж", "", 5, 12, 0, 3*time.Hour),
	}
	calendar.Sort(events)
	return events
}
