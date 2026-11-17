// Package calendar reads published iCalendar feeds (an iPhone calendar shared
// as a public iCloud link, a Google calendar's secret address) and lists the
// events of a time window, recurring ones included.
package calendar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// Event is one occurrence. All-day events start and end at midnight in the
// zone they were listed for, End exclusive, so a one-day event lasts 24 hours
// (23 or 25 on a DST change).
type Event struct {
	Title    string
	Location string
	Start    time.Time
	End      time.Time
	AllDay   bool
}

// Calendar is a parsed feed. It is read-only once loaded, so one Calendar can
// serve concurrent requests for different time zones.
type Calendar struct {
	events []ical.Event
}

// Parse reads an iCalendar document.
func Parse(r io.Reader) (*Calendar, error) {
	cal, err := ical.NewDecoder(r).Decode()
	if err != nil {
		return nil, fmt.Errorf("parse calendar: %w", err)
	}
	return &Calendar{events: cal.Events()}, nil
}

// MaxFeedBytes bounds a downloaded feed. Years of history in one calendar
// stay well under it; past it the feed is refused rather than cut short,
// which would silently lose the events at the end.
const MaxFeedBytes = 8 << 20

// Fetcher downloads feeds.
type Fetcher struct {
	http *http.Client
}

// NewFetcher returns a Fetcher using client; see NewClient.
func NewFetcher(client *http.Client) *Fetcher {
	return &Fetcher{http: client}
}

// NewClient returns the HTTP client for feeds: https redirects only, at most five.
func NewClient() *http.Client {
	return &http.Client{
		Timeout: 15 * time.Second, // under the API server's 20 s write timeout
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect away from https")
			}
			return nil
		},
	}
}

// Load downloads and parses the feed at rawURL (https, as ParseURL returns).
//
// A feed URL is a credential: whoever has it reads the calendar. Errors name
// only the host, never the path that carries the token, because they end up
// in logs.
func (f *Fetcher) Load(ctx context.Context, rawURL string) (*Calendar, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("calendar: bad URL")
	}
	fail := func(err error) error { return fmt.Errorf("calendar %s: %w", u.Host, err) }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fail(errors.New("bad URL"))
	}
	req.Header.Set("Accept", "text/calendar")
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := f.http.Do(req)
	if err != nil {
		// *url.Error prints the whole URL; keep only what went wrong.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fail(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fail(fmt.Errorf("unexpected status %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFeedBytes+1))
	if err != nil {
		return nil, fail(err)
	}
	if len(body) > MaxFeedBytes {
		return nil, fail(fmt.Errorf("feed is larger than %d MiB", MaxFeedBytes>>20))
	}
	cal, err := Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fail(err)
	}
	return cal, nil
}

// ParseURL checks a feed address as a user pastes it and returns the form to
// fetch. iPhone shares a calendar as webcal://, which is https underneath.
func ParseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("must not be empty")
	}
	if len(raw) > 2048 {
		return "", errors.New("must be at most 2048 bytes")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("must be a webcal:// or https:// link")
	}
	switch strings.ToLower(u.Scheme) {
	case "webcal", "webcals", "https":
		u.Scheme = "https"
	default:
		return "", errors.New("must be a webcal:// or https:// link")
	}
	if u.User != nil {
		return "", errors.New("must not contain a user name or password")
	}
	u.Fragment = ""
	return u.String(), nil
}

// Expansion limits: a feed is the owner's own, but a bad rule shouldn't be
// able to stall a request.
const (
	maxPerSeries = 100 // occurrences of one recurring event inside the window
	minFrequency = rrule.HOURLY
)

// Between returns the events that overlap [from, to), sorted by start time.
// loc is the zone of the person looking: all-day events and times without a
// zone ("floating", as in a local alarm) are placed in it.
//
// skipped counts events that couldn't be read (an unknown time zone, a broken
// rule); they are left out instead of failing the whole calendar.
func (c *Calendar) Between(from, to time.Time, loc *time.Location) (events []Event, skipped int) {
	// A changed occurrence of a series (moved, retitled, cancelled) is its own
	// VEVENT with the series' UID and a RECURRENCE-ID naming the occurrence it
	// replaces. Collect those first so the series skips them.
	replaced := map[string]map[int64]bool{}
	for _, ev := range c.events {
		rid := ev.Props.Get(ical.PropRecurrenceID)
		if rid == nil {
			continue
		}
		t, err := rid.DateTime(loc)
		if err != nil {
			skipped++
			continue
		}
		uid := uidOf(ev)
		if replaced[uid] == nil {
			replaced[uid] = map[int64]bool{}
		}
		replaced[uid][t.Unix()] = true
	}

	for _, ev := range c.events {
		if status, _ := ev.Props.Text(ical.PropStatus); strings.EqualFold(status, string(ical.EventCancelled)) {
			continue
		}
		occ, err := occurrences(ev, from, to, loc, replaced[uidOf(ev)])
		if err != nil {
			skipped++
			continue
		}
		events = append(events, occ...)
	}
	Sort(events)
	return events, skipped
}

// Sort orders events by start, all-day ones first on a tie, as calendar apps
// list them; for merging several calendars.
func Sort(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		return a.Title < b.Title
	})
}

func uidOf(ev ical.Event) string {
	uid, _ := ev.Props.Text(ical.PropUID)
	return uid
}

// occurrences lists one VEVENT's occurrences that overlap [from, to).
func occurrences(ev ical.Event, from, to time.Time, loc *time.Location, replaced map[int64]bool) ([]Event, error) {
	startProp := ev.Props.Get(ical.PropDateTimeStart)
	if startProp == nil {
		return nil, errors.New("no DTSTART")
	}
	start, err := startProp.DateTime(loc)
	if err != nil {
		return nil, err
	}
	end, err := ev.DateTimeEnd(loc)
	if err != nil {
		return nil, err
	}
	allDay := isDate(startProp)
	// All-day lengths are counted in days, so a series crossing a DST change
	// still ends at midnight.
	days := 0
	if allDay {
		days = int(end.Sub(start).Round(24*time.Hour) / (24 * time.Hour))
		if days < 1 {
			days = 1
		}
	}
	length := end.Sub(start)
	if length < 0 {
		length = 0
	}
	title, _ := ev.Props.Text(ical.PropSummary)
	place, _ := ev.Props.Text(ical.PropLocation)
	at := func(s time.Time) Event {
		e := Event{Title: strings.TrimSpace(title), Location: strings.TrimSpace(place), Start: s, End: s.Add(length), AllDay: allDay}
		if allDay {
			e.End = s.AddDate(0, 0, days)
		}
		return e
	}

	rule := ev.Props.Get(ical.PropRecurrenceRule)
	// An override (it has a RECURRENCE-ID) is a single occurrence even when
	// it repeats the series' RRULE, as some clients write it.
	if rule == nil || ev.Props.Get(ical.PropRecurrenceID) != nil {
		e := at(start)
		if !overlaps(e, from, to) {
			return nil, nil
		}
		return []Event{e}, nil
	}

	set, err := recurrence(ev, start, loc)
	if err != nil {
		return nil, err
	}
	// Starts before from still count while the occurrence runs into the window.
	lookback := length
	if allDay {
		lookback = time.Duration(days)*24*time.Hour + time.Hour
	}
	var out []Event
	for _, s := range set.Between(from.Add(-lookback), to, true) {
		if replaced[s.Unix()] {
			continue
		}
		e := at(s)
		if !overlaps(e, from, to) {
			continue
		}
		out = append(out, e)
		if len(out) == maxPerSeries {
			break
		}
	}
	return out, nil
}

// recurrence builds the series of a VEVENT with an RRULE. It reads EXDATE and
// RDATE itself, because a single line may list several dates separated by
// commas (go-ical's RecurrenceSet expects one per line).
func recurrence(ev ical.Event, start time.Time, loc *time.Location) (*rrule.Set, error) {
	opt, err := ev.Props.RecurrenceRule()
	if err != nil {
		return nil, err
	}
	if opt.Freq > minFrequency { // rrule counts down: YEARLY is 0, SECONDLY 6
		return nil, fmt.Errorf("frequency %s is not supported", opt.Freq)
	}
	opt.Dtstart = start
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	set := &rrule.Set{}
	set.RRule(rule)
	for _, name := range []string{ical.PropExceptionDates, ical.PropRecurrenceDates} {
		for _, prop := range ev.Props.Values(name) {
			if strings.EqualFold(prop.Params.Get(ical.ParamValue), string(ical.ValuePeriod)) {
				continue // RDATE as a period: rare, and its own length; leave it out
			}
			for _, v := range strings.Split(prop.Value, ",") {
				one := prop
				one.Value = strings.TrimSpace(v)
				t, err := one.DateTime(loc)
				if err != nil {
					return nil, err
				}
				if name == ical.PropExceptionDates {
					set.ExDate(t)
				} else {
					set.RDate(t)
				}
			}
		}
	}
	return set, nil
}

// isDate tells a DATE start (an all-day event) from a DATE-TIME one. It reads
// the VALUE parameter itself: Prop.ValueType falls back to DTSTART's default
// type, DATE-TIME, when the parameter is absent.
func isDate(p *ical.Prop) bool {
	if v := p.Params.Get(ical.ParamValue); v != "" {
		return strings.EqualFold(v, string(ical.ValueDate))
	}
	return len(p.Value) == len("20060102")
}

func overlaps(e Event, from, to time.Time) bool {
	if !e.Start.Before(to) {
		return false
	}
	if e.End.After(e.Start) {
		return e.End.After(from)
	}
	return !e.Start.Before(from) // a moment, such as a reminder
}
