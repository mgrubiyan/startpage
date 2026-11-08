package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/store"
)

type fakeNotifier struct {
	mu   sync.Mutex
	sent []string
	fail bool
}

func (f *fakeNotifier) Send(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("telegram: unreachable")
	}
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeNotifier) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

var (
	bot = store.Service{ID: 1, Name: "bot", URL: "http://bot.internal/health"}
	api = store.Service{ID: 2, Name: "api", URL: "http://api.internal/health"}
	t0  = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
)

func up(svc store.Service, at time.Duration) store.Check {
	return store.Check{ServiceID: svc.ID, CheckedAt: t0.Add(at), OK: true, StatusCode: 200}
}

func fail(svc store.Service, at time.Duration) store.Check {
	return store.Check{ServiceID: svc.ID, CheckedAt: t0.Add(at), Error: "timeout"}
}

func TestTransitions(t *testing.T) {
	type round struct {
		checks []store.Check // one per listed service, in order
		want   []string
	}
	for _, tc := range []struct {
		name   string
		seed   []store.ServiceStatus
		rounds []round
	}{
		{
			name: "one lost probe is not an outage",
			rounds: []round{
				{checks: []store.Check{fail(bot, 0)}},
				{checks: []store.Check{up(bot, time.Minute)}},
				{checks: []store.Check{fail(bot, 2*time.Minute)}},
				{checks: []store.Check{up(bot, 3*time.Minute)}},
			},
		},
		{
			name: "down after two failures, once, then up with the downtime",
			rounds: []round{
				{checks: []store.Check{fail(bot, 0)}},
				{checks: []store.Check{fail(bot, time.Minute)}, want: []string{"🔴 bot недоступен: timeout"}},
				{checks: []store.Check{fail(bot, 2*time.Minute)}},
				{checks: []store.Check{up(bot, 12*time.Minute)}, want: []string{"🟢 bot снова работает (не работал 12 мин)"}},
				{checks: []store.Check{up(bot, 13*time.Minute)}},
			},
		},
		{
			name: "each service on its own",
			rounds: []round{
				{checks: []store.Check{fail(bot, 0), up(api, 0)}},
				{checks: []store.Check{fail(bot, time.Minute), fail(api, time.Minute)}, want: []string{"🔴 bot недоступен: timeout"}},
				{checks: []store.Check{up(bot, 2*time.Minute), fail(api, 2*time.Minute)},
					want: []string{"🟢 bot снова работает (не работал 2 мин)", "🔴 api недоступен: timeout"}},
			},
		},
		{
			name: "down before a restart: no repeat, recovery still reported",
			seed: []store.ServiceStatus{{Service: bot, Last: &store.Check{OK: false}, TotalChecks: 3}},
			rounds: []round{
				{checks: []store.Check{fail(bot, 0)}},
				{checks: []store.Check{fail(bot, time.Minute)}},
				{checks: []store.Check{up(bot, 2*time.Minute)}, want: []string{"🟢 bot снова работает"}},
			},
		},
		{
			name: "up or never checked before a restart: alerts as usual",
			seed: []store.ServiceStatus{{Service: bot, Last: &store.Check{OK: true}}, {Service: api}},
			rounds: []round{
				{checks: []store.Check{fail(bot, 0), fail(api, 0)}},
				{checks: []store.Check{fail(bot, time.Minute), fail(api, time.Minute)},
					want: []string{"🔴 bot недоступен: timeout", "🔴 api недоступен: timeout"}},
			},
		},
		{
			// One failure, or too few checks to be sure, is not a reported outage:
			// staying quiet here would swallow it.
			name: "unconfirmed failure before a restart: the next failure alerts",
			seed: []store.ServiceStatus{
				{Service: bot, Last: &store.Check{OK: false, CheckedAt: t0.Add(-time.Minute)}, TotalChecks: 3, OKChecks: 2},
				{Service: api, Last: &store.Check{OK: false, CheckedAt: t0.Add(-time.Minute)}, TotalChecks: 1},
			},
			rounds: []round{
				{checks: []store.Check{fail(bot, 0), fail(api, 0)},
					want: []string{"🔴 bot недоступен: timeout", "🔴 api недоступен: timeout"}},
				{checks: []store.Check{up(bot, 10*time.Minute), fail(api, 10*time.Minute)},
					want: []string{"🟢 bot снова работает (не работал 11 мин)"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			fs.statuses = tc.seed
			m := New(fs, quietLogger(), Config{Notifier: &fakeNotifier{}})
			m.seed(context.Background())
			for i, r := range tc.rounds {
				services := []store.Service{bot, api}[:len(r.checks)]
				got := m.transitions(services, r.checks)
				if strings.Join(got, "|") != strings.Join(r.want, "|") {
					t.Errorf("round %d: alerts = %q, want %q", i+1, got, r.want)
				}
			}
		})
	}
}

func TestSeedLooksBackAFewRounds(t *testing.T) {
	fs := newFakeStore()
	m := New(fs, quietLogger(), Config{Interval: time.Minute, Notifier: &fakeNotifier{}})
	m.seed(context.Background())
	want := time.Now().Add(-(downAfter + 1) * time.Minute)
	if d := fs.since.Sub(want); d < -5*time.Second || d > 5*time.Second {
		t.Errorf("seeded from checks since %s, want about %s", fs.since, want)
	}
}

func TestTransitionsForgetDeletedServices(t *testing.T) {
	m := New(newFakeStore(), quietLogger(), Config{Notifier: &fakeNotifier{}})
	m.transitions([]store.Service{bot}, []store.Check{fail(bot, 0)})
	m.transitions([]store.Service{bot}, []store.Check{fail(bot, time.Minute)})
	m.transitions([]store.Service{api}, []store.Check{up(api, 2*time.Minute)}) // bot deleted

	if _, ok := m.health[bot.ID]; ok {
		t.Fatal("a deleted service is still tracked")
	}
	// Re-added under the same id, it starts clean: no stray "back up" alert.
	if got := m.transitions([]store.Service{bot}, []store.Check{up(bot, 3*time.Minute)}); len(got) != 0 {
		t.Errorf("alerts = %q, want none", got)
	}
}

// TestRoundAlertsAndRetries drives real probe rounds against a service that goes
// down and comes back, while Telegram is briefly unreachable.
func TestRoundAlertsAndRetries(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	n := &fakeNotifier{}
	m := New(newFakeStore(store.Service{ID: 1, Name: "bot", URL: srv.URL}), quietLogger(), Config{Notifier: n})
	ctx := context.Background()

	m.round(ctx) // first failure
	n.fail = true
	m.round(ctx) // down, but the send fails
	if len(n.Sent()) != 0 || len(m.pending) != 1 {
		t.Fatalf("sent %q, pending %q: want nothing sent and the alert kept", n.Sent(), m.pending)
	}
	n.fail = false
	m.round(ctx) // still down: the kept alert goes out now
	healthy.Store(true)
	m.round(ctx) // back up

	sent := n.Sent()
	if len(sent) != 2 || sent[0] != "🔴 bot недоступен: HTTP 503 Service Unavailable" ||
		!strings.HasPrefix(sent[1], "🟢 bot снова работает (не работал ") {
		t.Errorf("sent = %q", sent)
	}
	if len(m.pending) != 0 {
		t.Errorf("pending = %q after a successful send", m.pending)
	}
}

func TestRoundCutByShutdownAlertsNothing(t *testing.T) {
	n := &fakeNotifier{}
	m := New(newFakeStore(store.Service{ID: 1, Name: "bot", URL: "http://127.0.0.1:1/"}), quietLogger(), Config{Notifier: n})
	m.health[1] = &health{fails: 1, since: t0} // one more failure would alert

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.round(ctx)

	if h := m.health[1]; len(n.Sent()) != 0 || len(m.pending) != 0 || h.fails != 1 || h.down {
		t.Errorf("sent %q, pending %q, health %+v: a cancelled round must change nothing", n.Sent(), m.pending, *h)
	}
}

func TestFlushKeepsTheNewestAlerts(t *testing.T) {
	m := New(newFakeStore(), quietLogger(), Config{Notifier: &fakeNotifier{fail: true}})
	for i := range maxPending + 10 {
		m.pending = append(m.pending, fmt.Sprint(i))
	}
	m.flush(context.Background())
	if len(m.pending) != maxPending || m.pending[0] != "10" {
		t.Errorf("pending = %d alerts from %q, want the newest %d", len(m.pending), m.pending[0], maxPending)
	}
}

func TestHumanize(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                             "меньше минуты",
		59 * time.Second:              "меньше минуты",
		time.Minute:                   "1 мин",
		90 * time.Second:              "2 мин",
		59 * time.Minute:              "59 мин",
		time.Hour:                     "1 ч",
		time.Hour + 5*time.Minute:     "1 ч 5 мин",
		26*time.Hour + 30*time.Second: "26 ч 1 мин",
		2*time.Hour + 59*time.Minute + 45*time.Second: "3 ч",
	} {
		if got := humanize(d); got != want {
			t.Errorf("humanize(%s) = %q, want %q", d, got, want)
		}
	}
}
