package art

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePicker struct {
	calls int
	days  []string
	err   error
}

func (f *fakePicker) pick(_ context.Context, day string) (Artwork, error) {
	f.calls++
	f.days = append(f.days, day)
	if f.err != nil {
		return Artwork{}, f.err
	}
	return Artwork{ID: f.calls, Title: day}, nil
}

func newTestDaily(t *testing.T) (*Daily, *fakePicker, *time.Time) {
	t.Helper()
	p := &fakePicker{}
	now := time.Date(2026, 11, 8, 23, 0, 0, 0, time.FixedZone("MSK", 3*3600)) // 20:00 UTC
	d := NewDaily(p.pick)
	d.now = func() time.Time { return now }
	return d, p, &now
}

func TestDailyLoadsOncePerUTCDay(t *testing.T) {
	d, p, now := newTestDaily(t)
	ctx := context.Background()
	for range 3 {
		if a, err := d.Today(ctx); err != nil || a.ID != 1 {
			t.Fatalf("Today = %+v, %v", a, err)
		}
	}
	*now = now.Add(5 * time.Hour) // 01:00 UTC on the 9th
	if a, err := d.Today(ctx); err != nil || a.ID != 2 {
		t.Fatalf("next day: Today = %+v, %v", a, err)
	}
	if p.calls != 2 || p.days[0] != "2026-11-08" || p.days[1] != "2026-11-09" {
		t.Fatalf("loads: %d %v", p.calls, p.days)
	}
}

func TestDailyKeepsYesterdayWhenTheMuseumIsDown(t *testing.T) {
	d, p, now := newTestDaily(t)
	ctx := context.Background()
	if _, err := d.Today(ctx); err != nil {
		t.Fatal(err)
	}
	p.err = errors.New("museum down")
	*now = now.Add(24 * time.Hour)
	for range 3 {
		if a, err := d.Today(ctx); err != nil || a.ID != 1 {
			t.Fatalf("Today = %+v, %v; want yesterday's painting", a, err)
		}
	}
	if p.calls != 2 {
		t.Fatalf("%d loads during the backoff, want 1 retry", p.calls-1)
	}
	p.err = nil
	*now = now.Add(d.backoff)
	if a, _ := d.Today(ctx); a.ID != 3 {
		t.Fatalf("after the backoff: Today = %+v, want a fresh load", a)
	}
}

func TestDailyWithoutAnyPainting(t *testing.T) {
	d, p, now := newTestDaily(t)
	p.err = errors.New("museum down")
	ctx := context.Background()
	if _, err := d.Today(ctx); !errors.Is(err, p.err) {
		t.Fatalf("first failure: err = %v", err)
	}
	if _, err := d.Today(ctx); !errors.Is(err, ErrUnavailable) || p.calls != 1 {
		t.Fatalf("during the backoff: err = %v after %d loads", err, p.calls)
	}
	p.err = nil
	*now = now.Add(d.backoff)
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
