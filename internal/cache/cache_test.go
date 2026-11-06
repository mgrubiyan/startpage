package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)} }

func TestFreshHitDoesNotReload(t *testing.T) {
	clock := newClock()
	var calls atomic.Int32
	c := New(time.Minute, func(_ context.Context, key string) (string, error) {
		calls.Add(1)
		return "v:" + key, nil
	}, WithClock(clock.Now))

	for range 3 {
		r, err := c.Get(context.Background(), "spb")
		if err != nil || r.Value != "v:spb" || r.Stale {
			t.Fatalf("Get = %+v, %v", r, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}

	clock.Advance(time.Minute)
	if _, err := c.Get(context.Background(), "spb"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expired entry not reloaded: %d calls", calls.Load())
	}
}

func TestFailureServesStaleAndIsNotCached(t *testing.T) {
	clock := newClock()
	var fail atomic.Bool
	var calls atomic.Int32
	c := New(time.Minute, func(context.Context, string) (int, error) {
		calls.Add(1)
		if fail.Load() {
			return 0, errors.New("upstream down")
		}
		return 42, nil
	}, WithClock(clock.Now))

	first, err := c.Get(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}

	fail.Store(true)
	clock.Advance(2 * time.Minute)
	r, err := c.Get(context.Background(), "k")
	if err != nil || !r.Stale || r.Value != 42 || !r.FetchedAt.Equal(first.FetchedAt) {
		t.Fatalf("stale fallback = %+v, %v", r, err)
	}

	// The failure must not have refreshed the entry: the next call retries upstream.
	fail.Store(false)
	r, err = c.Get(context.Background(), "k")
	if err != nil || r.Stale || calls.Load() != 3 {
		t.Fatalf("after recovery = %+v, %v, calls=%d", r, err, calls.Load())
	}

	fail.Store(true)
	if _, err := c.Get(context.Background(), "never-loaded"); err == nil {
		t.Fatal("failure without a cached value must return the error")
	}
}

func TestConcurrentMissesShareOneLoad(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	c := New(time.Minute, func(context.Context, string) (int, error) {
		calls.Add(1)
		<-release
		return 7, nil
	})

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Get(context.Background(), "same")
			if err == nil && r.Value != 7 {
				err = fmt.Errorf("value %d", r.Value)
			}
			errs <- err
		}()
	}
	// Let the goroutines pile up on the in-flight load before releasing it.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times for %d concurrent misses, want 1", calls.Load(), n)
	}
}

func TestCallerCancelDoesNotAbortSharedLoad(t *testing.T) {
	release := make(chan struct{})
	c := New(time.Minute, func(ctx context.Context, _ string) (string, error) {
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := c.Get(ctx, "k")
		firstDone <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller got %v", err)
	}

	secondDone := make(chan string, 1)
	go func() {
		r, _ := c.Get(context.Background(), "k")
		secondDone <- r.Value
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if v := <-secondDone; v != "done" {
		t.Fatalf("second caller got %q: the shared load died with the first caller", v)
	}
}

func TestLoadTimeout(t *testing.T) {
	c := New(time.Minute, func(ctx context.Context, _ string) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}, WithLoadTimeout(20*time.Millisecond))
	_, err := c.Get(context.Background(), "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestEvictionBoundsSize(t *testing.T) {
	clock := newClock()
	c := New(time.Hour, func(_ context.Context, key string) (string, error) { return key, nil },
		WithClock(clock.Now), WithMaxEntries(3))
	for i := range 10 {
		clock.Advance(time.Second)
		if _, err := c.Get(context.Background(), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	c.mu.Lock()
	_, newest := c.items["9"]
	_, oldest := c.items["0"]
	c.mu.Unlock()
	if !newest || oldest {
		t.Fatalf("eviction kept wrong keys: newest=%v oldest=%v", newest, oldest)
	}
}
