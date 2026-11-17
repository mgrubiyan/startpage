// Package digest sends a morning summary to Telegram once a day: the weather,
// today's events, the CBR rates and any service that is down. It reads the
// same cached sources as the page, so it adds no requests of its own.
package digest

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/calendar"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/store"
	"github.com/mgrubiyan/startpage/internal/weather"
)

// Sources are where the digest takes its parts from. Any of them may fail:
// that part is left out and the rest still goes.
type Sources struct {
	Settings func(ctx context.Context) (store.Settings, error)
	Weather  func(ctx context.Context, city string) (weather.Report, error)
	Rates    func(ctx context.Context) (rates.Rates, error)
	// Calendar loads one feed; nil when calendars aren't available.
	Calendar func(ctx context.Context, link string) (*calendar.Calendar, error)
	Services func(ctx context.Context, since time.Time) ([]store.ServiceStatus, error)
}

// Sender delivers the message (telegram.Bot).
type Sender interface {
	Send(ctx context.Context, text string) error
}

// Digest sends the summary every day at a set time in a set zone.
type Digest struct {
	src  Sources
	send Sender
	at   int // minutes after midnight
	loc  *time.Location
	log  *slog.Logger
	now  func() time.Time
}

// New returns a Digest sent at minute at (after midnight) of each day in loc.
func New(src Sources, send Sender, at int, loc *time.Location, log *slog.Logger) *Digest {
	return &Digest{src: src, send: send, at: at, loc: loc, log: log, now: time.Now}
}

// Run sends the digest each day until ctx is done. A restart after the hour
// waits for the next day: the day's digest is never sent twice.
func (d *Digest) Run(ctx context.Context) {
	for {
		wait := time.Until(Next(d.now(), d.at, d.loc))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		sendCtx, cancel := context.WithTimeout(ctx, time.Minute)
		if err := d.send.Send(sendCtx, d.Compose(sendCtx)); err != nil && ctx.Err() == nil {
			d.log.Warn("morning digest not sent", "err", err)
		} else if err == nil {
			d.log.Info("morning digest sent")
		}
		cancel()
	}
}

// Next is the first time after now that is minute at of a day in loc. Built
// from the date, so a DST change that day still lands on the wall-clock time.
func Next(now time.Time, at int, loc *time.Location) time.Time {
	n := now.In(loc)
	t := time.Date(n.Year(), n.Month(), n.Day(), at/60, at%60, 0, 0, loc)
	if !t.After(n) {
		t = time.Date(n.Year(), n.Month(), n.Day()+1, at/60, at%60, 0, 0, loc)
	}
	return t
}

// Compose builds today's message. The parts load side by side, each on its
// own: a slow or broken one is left out, not waited on past ctx.
func (d *Digest) Compose(ctx context.Context) string {
	now := d.now().In(d.loc)
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, d.loc)

	settings, err := d.src.Settings(ctx)
	if err != nil {
		d.log.Warn("digest: settings", "err", err)
	}

	var (
		wg                         sync.WaitGroup
		weatherPart, agenda, money string
		services                   string
	)
	wg.Go(func() { weatherPart = d.weather(ctx, settings.City) })
	wg.Go(func() { agenda = d.agenda(ctx, settings.Calendars, from) })
	wg.Go(func() { money = d.rates(ctx) })
	wg.Go(func() { services = d.services(ctx, now) })
	wg.Wait()

	parts := []string{"Доброе утро! " + longDate(now)}
	for _, p := range []string{weatherPart, agenda, money, services} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (d *Digest) weather(ctx context.Context, city string) string {
	if d.src.Weather == nil {
		return ""
	}
	w, err := d.src.Weather(ctx, city)
	if err != nil {
		d.log.Warn("digest: weather", "err", err)
		return ""
	}
	line := fmt.Sprintf("%s %s: сейчас %s, ветер %d м/с", w.Icon, w.Location, temp(w.TemperatureC), int(math.Round(w.WindMS)))
	if len(w.Days) > 0 {
		day := w.Days[0]
		line += fmt.Sprintf("\nДнём %s…%s, %s", temp(day.MinC), temp(day.MaxC), strings.ToLower(day.Description))
		if p := day.PrecipitationPct; p != nil && *p >= 30 {
			line += fmt.Sprintf(", осадки %d%%", *p)
		}
	}
	return line
}

// maxEvents keeps a busy day to a glance; the page has the rest.
const maxEvents = 8

func (d *Digest) agenda(ctx context.Context, links []string, from time.Time) string {
	if d.src.Calendar == nil || len(links) == 0 {
		return ""
	}
	to := from.AddDate(0, 0, 1)
	var events []calendar.Event
	failed := 0
	for _, link := range links {
		c, err := d.src.Calendar(ctx, link)
		if err != nil {
			failed++
			continue
		}
		evs, _ := c.Between(from, to, d.loc)
		events = append(events, evs...)
	}
	if failed == len(links) {
		d.log.Warn("digest: no calendar loaded", "calendars", len(links))
		return ""
	}
	calendar.Sort(events)
	if len(events) == 0 {
		return "📅 Сегодня ничего не запланировано"
	}
	lines := []string{"📅 Сегодня"}
	for i, e := range events {
		if i == maxEvents {
			lines = append(lines, fmt.Sprintf("и ещё %d", len(events)-maxEvents))
			break
		}
		switch {
		case e.AllDay:
			lines = append(lines, "весь день · "+e.Title)
		case e.Start.Before(from):
			lines = append(lines, fmt.Sprintf("до %s · %s", e.End.In(d.loc).Format("15:04"), e.Title))
		default:
			lines = append(lines, e.Start.In(d.loc).Format("15:04")+" "+e.Title)
		}
	}
	return strings.Join(lines, "\n")
}

func (d *Digest) rates(ctx context.Context) string {
	if d.src.Rates == nil {
		return ""
	}
	r, err := d.src.Rates(ctx)
	if err != nil {
		d.log.Warn("digest: rates", "err", err)
		return ""
	}
	return fmt.Sprintf("💵 ЦБ: $ %s · € %s", rate(r.USD), rate(r.EUR))
}

func (d *Digest) services(ctx context.Context, now time.Time) string {
	if d.src.Services == nil {
		return ""
	}
	list, err := d.src.Services(ctx, now.Add(-24*time.Hour))
	if err != nil {
		d.log.Warn("digest: services", "err", err)
		return ""
	}
	if len(list) == 0 {
		return ""
	}
	var down []string
	for _, s := range list {
		if s.Last != nil && !s.Last.OK {
			down = append(down, s.Name)
		}
	}
	switch {
	case len(down) == 1:
		return "⚠️ Не отвечает: " + down[0]
	case len(down) > 1:
		return "⚠️ Не отвечают: " + strings.Join(down, ", ")
	case len(list) == 1:
		return "✅ Сервис работает"
	default:
		return fmt.Sprintf("✅ Все %d %s работают", len(list), plural(len(list), "сервис", "сервиса", "сервисов"))
	}
}

// plural picks the Russian form for n: 1 сервис, 2 сервиса, 5 сервисов, 21 сервис.
func plural(n int, one, few, many string) string {
	switch n10, n100 := n%10, n%100; {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return few
	default:
		return many
	}
}

func temp(c float64) string {
	r := int(math.Round(c))
	if r > 0 {
		return fmt.Sprintf("+%d°", r)
	}
	return fmt.Sprintf("%d°", r) // 0 and below: "0°", "-3°"
}

// rate is "82,45 ▼0,06": the CBR value and how it moved since the day before.
func rate(r rates.Rate) string {
	s := comma(r.Value)
	switch diff := r.Value - r.Previous; {
	case diff >= 0.005:
		s += " ▲" + comma(diff)
	case diff <= -0.005:
		s += " ▼" + comma(-diff)
	}
	return s
}

func comma(v float64) string { return strings.Replace(fmt.Sprintf("%.2f", v), ".", ",", 1) }

var (
	weekdays = [...]string{"Воскресенье", "Понедельник", "Вторник", "Среда", "Четверг", "Пятница", "Суббота"}
	months   = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
)

// longDate is "Среда, 18 ноября", as the page shows it.
func longDate(t time.Time) string {
	return fmt.Sprintf("%s, %d %s", weekdays[t.Weekday()], t.Day(), months[t.Month()-1])
}
