// Package monitor periodically probes registered HTTP services and records the results.
//
// Probes go to user-configured URLs, including private addresses on purpose: the main
// use case is watching bots on the same host or Docker network. The endpoint that
// registers URLs is behind authentication, and each probe is bounded (timeout,
// redirect limit, body read limit), so the monitor cannot be used to pull data out.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
	"github.com/mgrubiyan/startpage/internal/store"
)

// Store is the persistence the monitor needs; *postgres.Store implements it.
type Store interface {
	Services(ctx context.Context) ([]store.Service, error)
	RecordChecks(ctx context.Context, checks []store.Check) error
	PruneChecks(ctx context.Context, before time.Time) (int64, error)
	ServiceStatuses(ctx context.Context, since time.Time) ([]store.ServiceStatus, error)
}

// Notifier delivers alerts when a service goes down or comes back;
// *telegram.Bot implements it.
type Notifier interface {
	Send(ctx context.Context, text string) error
}

// Config tunes the monitor.
type Config struct {
	Interval  time.Duration // time between rounds
	Timeout   time.Duration // per-probe timeout
	Workers   int           // max concurrent probes
	Retention time.Duration // how long check history is kept
	Notifier  Notifier      // nil: no alerts
}

const (
	maxRedirects  = 3
	maxBodyBytes  = 64 << 10
	maxErrorChars = 200
	pruneEvery    = time.Hour

	// downAfter failed checks in a row make a service down: one lost probe is not an outage.
	downAfter   = 2
	sendTimeout = 10 * time.Second
	// maxPending bounds the alerts kept for a retry while Telegram is unreachable.
	maxPending = 50
)

// health is what the monitor remembers about a service between rounds.
type health struct {
	fails int       // failed checks in a row
	down  bool      // a "down" alert is due or was sent
	since time.Time // first failed check of the outage; zero when unknown
}

// Monitor runs probe rounds on a ticker or on demand.
type Monitor struct {
	store   Store
	logger  *slog.Logger
	cfg     Config
	client  *http.Client
	trigger chan struct{}

	// Touched only by the Run goroutine.
	health  map[int64]*health
	pending []string // alerts not yet delivered
}

// New returns a Monitor. Zero Config fields get sensible defaults.
func New(s Store, logger *slog.Logger, cfg Config) *Monitor {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.Retention <= 0 {
		cfg.Retention = 7 * 24 * time.Hour
	}
	return &Monitor{
		store:  s,
		logger: logger,
		cfg:    cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return fmt.Errorf("stopped after %d redirects", maxRedirects)
				}
				return nil
			},
		},
		trigger: make(chan struct{}, 1),
		health:  make(map[int64]*health),
	}
}

// Trigger asks for a probe round as soon as possible. It never blocks;
// triggers arriving while a round is pending are coalesced into one.
func (m *Monitor) Trigger() {
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Run probes all services immediately and then every Interval until ctx is done.
// It returns only after in-flight probes have finished, so callers can close the
// database right after Run returns.
func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.Interval)
	defer ticker.Stop()

	if m.cfg.Notifier != nil {
		m.seed(ctx)
	}
	var lastPrune time.Time
	for {
		m.round(ctx)
		if time.Since(lastPrune) >= pruneEvery && ctx.Err() == nil {
			m.prune(ctx)
			lastPrune = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.trigger:
		}
	}
}

func (m *Monitor) round(ctx context.Context) {
	services, err := m.store.Services(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Error("monitor: list services", "err", err)
		}
		return
	}
	if len(services) == 0 {
		return
	}
	checks := m.probeAll(ctx, services)
	// Probes cut short by shutdown would be recorded as false outages; drop the round.
	if ctx.Err() != nil {
		return
	}
	if err := m.store.RecordChecks(ctx, checks); err != nil {
		m.logger.Error("monitor: record checks", "err", err)
	}
	if m.cfg.Notifier != nil {
		m.pending = append(m.pending, m.transitions(services, checks)...)
		m.flush(ctx)
	}
}

// seed starts from the checks recorded before a restart, so a deploy neither
// repeats the alert for a service that is still down nor misses its recovery.
// When in doubt it errs towards a duplicate alert, never a missed one.
func (m *Monitor) seed(ctx context.Context) {
	// The last few rounds tell a reported outage from one failure that wasn't yet.
	window := time.Duration(downAfter+1) * m.cfg.Interval
	statuses, err := m.store.ServiceStatuses(ctx, time.Now().Add(-window))
	if err != nil {
		if ctx.Err() == nil {
			m.logger.Warn("monitor: no previous state for alerts", "err", err)
		}
		return
	}
	for _, st := range statuses {
		switch {
		case st.Last == nil || st.Last.OK:
			// Up, or never checked: starts clean.
		case st.OKChecks == 0 && st.TotalChecks >= downAfter:
			// Failing throughout the window: the "down" alert already went out.
			m.health[st.ID] = &health{fails: downAfter, down: true}
		default:
			// A failure not confirmed yet: the next one alerts.
			m.health[st.ID] = &health{fails: 1, since: st.Last.CheckedAt}
		}
	}
}

// transitions updates each service's health with this round's checks and
// returns an alert for every service that went down or came back.
func (m *Monitor) transitions(services []store.Service, checks []store.Check) []string {
	var alerts []string
	listed := make(map[int64]bool, len(services))
	for i, svc := range services {
		listed[svc.ID] = true
		c := checks[i]
		h := m.health[svc.ID]
		if h == nil {
			h = &health{}
			m.health[svc.ID] = h
		}
		if c.OK {
			if h.down {
				alerts = append(alerts, upAlert(svc.Name, h.since, c.CheckedAt))
			}
			*h = health{}
			continue
		}
		if h.fails == 0 {
			h.since = c.CheckedAt
		}
		h.fails++
		if h.fails >= downAfter && !h.down {
			h.down = true
			alerts = append(alerts, fmt.Sprintf("🔴 %s недоступен: %s", svc.Name, c.Error))
		}
	}
	// A deleted service is forgotten, so one added later starts clean.
	for id := range m.health {
		if !listed[id] {
			delete(m.health, id)
		}
	}
	return alerts
}

// flush sends the pending alerts as one message. On failure they stay pending
// and go out with the next round, so a network blip doesn't lose an outage.
func (m *Monitor) flush(ctx context.Context) {
	if len(m.pending) == 0 {
		return
	}
	if len(m.pending) > maxPending {
		m.pending = m.pending[len(m.pending)-maxPending:]
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := m.cfg.Notifier.Send(sendCtx, strings.Join(m.pending, "\n")); err != nil {
		if ctx.Err() == nil {
			m.logger.Error("monitor: send alert, retrying next round", "err", err, "pending", len(m.pending))
		}
		return
	}
	m.pending = nil
}

func upAlert(name string, since, at time.Time) string {
	msg := "🟢 " + name + " снова работает"
	if !since.IsZero() {
		msg += " (не работал " + humanize(at.Sub(since)) + ")"
	}
	return msg
}

// humanize formats an outage length in Russian, to the minute.
func humanize(d time.Duration) string {
	if d < time.Minute {
		return "меньше минуты"
	}
	mins := int(d.Round(time.Minute).Minutes())
	switch h, m := mins/60, mins%60; {
	case h == 0:
		return fmt.Sprintf("%d мин", m)
	case m == 0:
		return fmt.Sprintf("%d ч", h)
	default:
		return fmt.Sprintf("%d ч %d мин", h, m)
	}
}

// probeAll fans services out to a bounded pool of workers.
func (m *Monitor) probeAll(ctx context.Context, services []store.Service) []store.Check {
	results := make([]store.Check, len(services))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(m.cfg.Workers, len(services)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = m.probe(ctx, services[i])
			}
		}()
	}
feed:
	for i := range services {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func (m *Monitor) probe(ctx context.Context, svc store.Service) store.Check {
	start := time.Now()
	check := store.Check{ServiceID: svc.ID, CheckedAt: start}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, svc.URL, nil)
	if err != nil {
		check.Error = truncate(err.Error())
		return check
	}
	req.Header.Set("User-Agent", fetch.UserAgent)

	resp, err := m.client.Do(req)
	check.Latency = time.Since(start)
	if err != nil {
		check.Error = describe(err)
		return check
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	_ = resp.Body.Close()

	check.StatusCode = resp.StatusCode
	check.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !check.OK {
		check.Error = "HTTP " + resp.Status
	}
	return check
}

func (m *Monitor) prune(ctx context.Context) {
	n, err := m.store.PruneChecks(ctx, time.Now().Add(-m.cfg.Retention))
	if err != nil {
		if ctx.Err() == nil { // a prune cut short by shutdown is not an error
			m.logger.Error("monitor: prune checks", "err", err)
		}
		return
	}
	if n > 0 {
		m.logger.Info("monitor: pruned old checks", "rows", n)
	}
}

// describe turns a client error into a short message without repeating the URL.
func describe(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return "timeout"
		}
		err = urlErr.Err
	}
	return truncate(err.Error())
}

func truncate(s string) string {
	r := []rune(s)
	if len(r) <= maxErrorChars {
		return s
	}
	return string(r[:maxErrorChars]) + "…"
}
