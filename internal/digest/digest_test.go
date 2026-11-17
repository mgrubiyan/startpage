package digest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/calendar"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/store"
	"github.com/mgrubiyan/startpage/internal/weather"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNext(t *testing.T) {
	msk := zone(t, "Europe/Moscow")
	berlin := zone(t, "Europe/Berlin")
	at := 8 * 60
	for name, c := range map[string]struct {
		now  time.Time
		loc  *time.Location
		want time.Time
	}{
		"before, same day": {time.Date(2026, 11, 18, 7, 59, 0, 0, msk), msk, time.Date(2026, 11, 18, 8, 0, 0, 0, msk)},
		"on the minute":    {time.Date(2026, 11, 18, 8, 0, 0, 0, msk), msk, time.Date(2026, 11, 19, 8, 0, 0, 0, msk)},
		"after, next day":  {time.Date(2026, 11, 18, 21, 0, 0, 0, msk), msk, time.Date(2026, 11, 19, 8, 0, 0, 0, msk)},
		"server in UTC":    {time.Date(2026, 11, 18, 4, 30, 0, 0, time.UTC), msk, time.Date(2026, 11, 18, 8, 0, 0, 0, msk)},
		// The clocks go forward at 02:00 that night: still 08:00 on the wall.
		"DST change": {time.Date(2026, 3, 28, 9, 0, 0, 0, berlin), berlin, time.Date(2026, 3, 29, 8, 0, 0, 0, berlin)},
	} {
		if got := Next(c.now, at, c.loc); !got.Equal(c.want) {
			t.Errorf("%s: Next = %v, want %v", name, got, c.want)
		}
	}
}

const ics = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//test//EN
BEGIN:VEVENT
UID:1
DTSTAMP:20261101T000000Z
DTSTART:20261118T070000Z
DTEND:20261118T073000Z
SUMMARY:Созвон
END:VEVENT
BEGIN:VEVENT
UID:2
DTSTAMP:20261101T000000Z
DTSTART;VALUE=DATE:20261118
DTEND;VALUE=DATE:20261119
SUMMARY:День рождения
END:VEVENT
BEGIN:VEVENT
UID:3
DTSTAMP:20261101T000000Z
DTSTART:20261119T070000Z
DTEND:20261119T073000Z
SUMMARY:Завтрашнее
END:VEVENT
END:VCALENDAR
`

func TestCompose(t *testing.T) {
	msk := zone(t, "Europe/Moscow")
	cal, err := calendar.Parse(strings.NewReader(strings.ReplaceAll(ics, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	rain := 80
	src := Sources{
		Settings: func(context.Context) (store.Settings, error) {
			return store.Settings{City: "Санкт-Петербург", Calendars: []string{"https://cal.example/a"}}, nil
		},
		Weather: func(_ context.Context, city string) (weather.Report, error) {
			return weather.Report{Location: city, Icon: "🌧", TemperatureC: 2.6, WindMS: 5.4, Days: []weather.Day{
				{MinC: -0.4, MaxC: 4.5, Description: "Дождь", PrecipitationPct: &rain},
			}}, nil
		},
		Rates: func(context.Context) (rates.Rates, error) {
			return rates.Rates{USD: rates.Rate{Value: 82.45, Previous: 82.51}, EUR: rates.Rate{Value: 90.81, Previous: 90.81}}, nil
		},
		Calendar: func(context.Context, string) (*calendar.Calendar, error) { return cal, nil },
		Services: func(context.Context, time.Time) ([]store.ServiceStatus, error) {
			return []store.ServiceStatus{
				{Service: store.Service{Name: "Бот"}, Last: &store.Check{OK: true}},
				{Service: store.Service{Name: "Парсер"}, Last: &store.Check{OK: false}},
			}, nil
		},
	}
	d := New(src, nil, 8*60, msk, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.now = func() time.Time { return time.Date(2026, 11, 18, 8, 0, 0, 0, msk) }

	want := strings.Join([]string{
		"Доброе утро! Среда, 18 ноября",
		"🌧 Санкт-Петербург: сейчас +3°, ветер 5 м/с\nДнём 0°…+5°, дождь, осадки 80%",
		"📅 Сегодня\nвесь день · День рождения\n10:00 Созвон",
		"💵 ЦБ: $ 82,45 ▼0,06 · € 90,81",
		"⚠️ Не отвечает: Парсер",
	}, "\n\n")
	if got := d.Compose(context.Background()); got != want {
		t.Errorf("Compose =\n%s\n\nwant\n%s", got, want)
	}

	// Every source down: the greeting still goes, nothing else is made up.
	fail := errors.New("down")
	d.src = Sources{
		Settings: func(context.Context) (store.Settings, error) { return store.Settings{}, fail },
		Weather:  func(context.Context, string) (weather.Report, error) { return weather.Report{}, fail },
		Rates:    func(context.Context) (rates.Rates, error) { return rates.Rates{}, fail },
		Services: func(context.Context, time.Time) ([]store.ServiceStatus, error) { return nil, fail },
	}
	if got := d.Compose(context.Background()); got != "Доброе утро! Среда, 18 ноября" {
		t.Errorf("all down: %q", got)
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{1: "сервис", 2: "сервиса", 4: "сервиса", 5: "сервисов", 11: "сервисов", 12: "сервисов", 21: "сервис", 22: "сервиса"} {
		if got := plural(n, "сервис", "сервиса", "сервисов"); got != want {
			t.Errorf("plural(%d) = %s, want %s", n, got, want)
		}
	}
}
