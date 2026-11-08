package art

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePicker returns paintings numbered by load; loads may run in the background.
type fakePicker struct {
	mu    sync.Mutex
	calls int
	days  []string
	err   error
	gate  map[string]chan struct{} // if set for a day, its load waits until the channel closes
	// entered receives the day of each gated load once it is waiting at its gate.
	entered chan string
}

func (f *fakePicker) pick(_ context.Context, day string) (Artwork, error) {
	f.mu.Lock()
	gate := f.gate[day]
	f.mu.Unlock()
	if gate != nil {
		f.entered <- day
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.days = append(f.days, day)
	if f.err != nil {
		return Artwork{}, f.err
	}
	return Artwork{ID: f.calls, Title: day}, nil
}

func (f *fakePicker) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePicker) loads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestDaily(t *testing.T) (*Daily, *fakePicker, *clock) {
	t.Helper()
	p := &fakePicker{gate: map[string]chan struct{}{}, entered: make(chan string, 4)}
	c := &clock{now: time.Date(2026, 11, 8, 23, 0, 0, 0, time.FixedZone("MSK", 3*3600))} // 20:00 UTC
	d := NewDaily(p.pick)
	d.now = c.Now
	return d, p, c
}

// eventually polls cond, for loads that finish in the background.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func todayID(d *Daily) int {
	a, _ := d.Today(context.Background())
	return a.ID
}

func TestDailyLoadsOncePerUTCDay(t *testing.T) {
	d, p, clk := newTestDaily(t)
	for range 3 {
		if a, err := d.Today(context.Background()); err != nil || a.ID != 1 {
			t.Fatalf("Today = %+v, %v", a, err)
		}
	}
	clk.Add(5 * time.Hour) // 01:00 UTC on the 9th
	eventually(t, "the next day's painting", func() bool { return todayID(d) == 2 })
	if p.loads() != 2 || p.days[0] != "2026-11-08" || p.days[1] != "2026-11-09" {
		t.Fatalf("loads: %d %v", p.loads(), p.days)
	}
}

// At midnight nobody waits for the download: yesterday's painting is served
// at once while today's loads.
func TestDailyNewDayDoesNotBlock(t *testing.T) {
	d, p, clk := newTestDaily(t)
	if todayID(d) != 1 {
		t.Fatal("first load failed")
	}
	release := make(chan struct{})
	p.gate["2026-11-09"] = release
	clk.Add(24 * time.Hour)

	done := make(chan int)
	go func() { done <- todayID(d) }()
	select {
	case id := <-done:
		if id != 1 {
			t.Fatalf("during the load: painting %d, want yesterday's", id)
		}
	case <-time.After(time.Second):
		t.Fatal("Today waited for the new day's download")
	}
	close(release)
	eventually(t, "today's painting", func() bool { return todayID(d) == 2 })
}

// Loads of two days overlap around midnight; the older one finishing last
// must not bring yesterday's painting back.
func TestDailyOlderLoadDoesNotWin(t *testing.T) {
	d, p, clk := newTestDaily(t)
	slow := make(chan struct{})
	p.gate["2026-11-08"] = slow
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _, _ = d.Today(ctx) }() // starts the 8th's load, which hangs
	<-p.entered
	cancel()

	clk.Add(24 * time.Hour)
	if a, err := d.Today(context.Background()); err != nil || a.Title != "2026-11-09" {
		t.Fatalf("Today = %+v, %v; want the 9th's painting", a, err)
	}
	close(slow)
	eventually(t, "the 8th's load to finish", func() bool { return p.loads() == 2 })
	if a, _ := d.Today(context.Background()); a.Title != "2026-11-09" {
		t.Fatalf("after the late load: %+v, want the 9th's painting", a)
	}
}

func TestDailyKeepsYesterdayWhenTheMuseumIsDown(t *testing.T) {
	d, p, clk := newTestDaily(t)
	if todayID(d) != 1 {
		t.Fatal("first load failed")
	}
	p.setErr(errors.New("museum down"))
	clk.Add(24 * time.Hour)
	if id := todayID(d); id != 1 {
		t.Fatalf("Today = %d, want yesterday's painting", id)
	}
	eventually(t, "the failed retry", func() bool { return p.loads() == 2 })
	for range 3 {
		if id := todayID(d); id != 1 {
			t.Fatalf("Today = %d, want yesterday's painting", id)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := p.loads(); n != 2 {
		t.Fatalf("%d loads during the backoff, want 1 retry", n-1)
	}
	p.setErr(nil)
	clk.Add(d.backoff)
	eventually(t, "a fresh load after the backoff", func() bool { return todayID(d) == 3 })
}

func TestDailyWithoutAnyPainting(t *testing.T) {
	d, p, clk := newTestDaily(t)
	down := errors.New("museum down")
	p.setErr(down)
	ctx := context.Background()
	if _, err := d.Today(ctx); !errors.Is(err, down) {
		t.Fatalf("first failure: err = %v", err)
	}
	if _, err := d.Today(ctx); !errors.Is(err, ErrUnavailable) || p.loads() != 1 {
		t.Fatalf("during the backoff: err = %v after %d loads", err, p.loads())
	}
	p.setErr(nil)
	clk.Add(d.backoff)
	if a, err := d.Today(ctx); err != nil || a.ID != 2 {
		t.Fatalf("after the backoff: Today = %+v, %v", a, err)
	}
}

// A caller that gives up must not cancel the shared load.
func TestDailyLoadSurvivesCallerCancel(t *testing.T) {
	release := make(chan struct{})
	var loadErr error
	d := NewDaily(func(ctx context.Context, _ string) (Artwork, error) {
		<-release
		loadErr = ctx.Err()
		return Artwork{ID: 1}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Today(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: err = %v", err)
	}
	close(release)
	if a, err := d.Today(context.Background()); err != nil || a.ID != 1 || loadErr != nil {
		t.Fatalf("Today = %+v, %v; load ctx err %v", a, err, loadErr)
	}
}
