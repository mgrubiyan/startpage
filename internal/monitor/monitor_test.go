package monitor

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/store"
)

type fakeStore struct {
	mu       sync.Mutex
	services []store.Service
	rounds   [][]store.Check
	pruned   []time.Time
	statuses []store.ServiceStatus // what the database remembers from before a restart
	since    time.Time             // the window ServiceStatuses was last asked for
	recorded chan struct{}
}

func newFakeStore(services ...store.Service) *fakeStore {
	return &fakeStore{services: services, recorded: make(chan struct{}, 100)}
}

func (f *fakeStore) Services(context.Context) ([]store.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Service(nil), f.services...), nil
}

func (f *fakeStore) RecordChecks(_ context.Context, checks []store.Check) error {
	f.mu.Lock()
	f.rounds = append(f.rounds, checks)
	f.mu.Unlock()
	f.recorded <- struct{}{}
	return nil
}

func (f *fakeStore) PruneChecks(_ context.Context, before time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pruned = append(f.pruned, before)
	return 0, nil
}

func (f *fakeStore) ServiceStatuses(_ context.Context, since time.Time) ([]store.ServiceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = since
	return append([]store.ServiceStatus(nil), f.statuses...), nil
}

func (f *fakeStore) Rounds() [][]store.Check {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]store.Check(nil), f.rounds...)
}

func (f *fakeStore) waitRound(t *testing.T) {
	t.Helper()
	select {
	case <-f.recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("no round recorded within 5s")
	}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestProbeClassification(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/down", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusMovedPermanently) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", 1<<20))) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	m := New(newFakeStore(), quietLogger(), Config{Timeout: 200 * time.Millisecond})
	tests := []struct {
		path      string
		wantOK    bool
		wantCode  int
		wantError string
	}{
		{"/ok", true, 204, ""},
		{"/moved", true, 204, ""},
		{"/big", true, 200, ""},
		{"/down", false, 503, "HTTP 503"},
		{"/slow", false, 0, "timeout"},
		{"/loop", false, 0, "stopped after 3 redirects"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			c := m.probe(context.Background(), store.Service{ID: 1, URL: srv.URL + tt.path})
			if c.OK != tt.wantOK || c.StatusCode != tt.wantCode || !strings.Contains(c.Error, tt.wantError) {
				t.Fatalf("check = %+v", c)
			}
			if c.ServiceID != 1 || c.CheckedAt.IsZero() || c.Latency <= 0 {
				t.Fatalf("metadata missing: %+v", c)
			}
			if strings.Contains(c.Error, srv.URL) {
				t.Fatalf("error repeats the URL: %q", c.Error)
			}
		})
	}

	c := m.probe(context.Background(), store.Service{ID: 2, URL: "http://127.0.0.1:1/unreachable"})
	if c.OK || c.Error == "" {
		t.Fatalf("connection refused must be a failure: %+v", c)
	}
}

func TestWorkersBoundConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		// Hold the request so concurrency can build up. The assertion below is an
		// upper bound, so a slow machine can only make the test weaker, never flaky.
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
	}))
	defer srv.Close()

	var services []store.Service
	for i := range 12 {
		services = append(services, store.Service{ID: int64(i + 1), URL: srv.URL})
	}
	m := New(newFakeStore(), quietLogger(), Config{Workers: 3, Timeout: time.Second})
	results := m.probeAll(context.Background(), services)

	if peak.Load() > 3 {
		t.Fatalf("peak concurrency %d exceeds 3 workers", peak.Load())
	}
	for i, r := range results {
		if !r.OK || r.ServiceID != services[i].ID {
			t.Fatalf("result %d = %+v: results must line up with services", i, r)
		}
	}
}

func TestRunProbesImmediatelyThenOnTrigger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	fs := newFakeStore(store.Service{ID: 1, URL: srv.URL})
	// Interval far beyond the test: every round after the first must come from Trigger.
	m := New(fs, quietLogger(), Config{Interval: time.Hour, Timeout: time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	fs.waitRound(t)
	m.Trigger()
	fs.waitRound(t)
	cancel()
	<-done

	if n := len(fs.Rounds()); n != 2 {
		t.Fatalf("rounds = %d, want 2", n)
	}
	fs.mu.Lock()
	pruned := len(fs.pruned)
	fs.mu.Unlock()
	if pruned != 1 {
		t.Fatalf("prune ran %d times, want once per hour", pruned)
	}
}

func TestTriggerNeverBlocks(t *testing.T) {
	m := New(newFakeStore(), quietLogger(), Config{})
	done := make(chan struct{})
	go func() {
		for range 1000 {
			m.Trigger()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Trigger blocked without a running monitor")
	}
}

func TestShutdownDropsInterruptedRound(t *testing.T) {
	started := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer srv.Close()
	fs := newFakeStore(store.Service{ID: 1, URL: srv.URL})
	m := New(fs, quietLogger(), Config{Interval: time.Hour, Timeout: 10 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	<-started
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return promptly after cancel")
	}
	if n := len(fs.Rounds()); n != 0 {
		t.Fatalf("interrupted round recorded as %d rounds: shutdown would look like an outage", n)
	}
}
