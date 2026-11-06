// Package cache is a small TTL cache for upstream API responses.
//
// Concurrent misses for one key are collapsed into a single upstream call
// (singleflight). Failed loads are never cached; if a previous value exists
// it is served as stale instead, so a flaky upstream does not blank the page.
// After a failed refresh the stale value is served immediately for a short
// backoff, so a dead upstream doesn't make every request wait out the timeout.
package cache

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Loader fetches the value for key from the upstream.
type Loader[V any] func(ctx context.Context, key string) (V, error)

// Result is a cached value plus its provenance.
type Result[V any] struct {
	Value     V
	FetchedAt time.Time
	// Stale is true when the upstream failed and an expired value was served instead.
	Stale bool
}

type entry[V any] struct {
	value     V
	fetchedAt time.Time
	failedAt  time.Time // last failed refresh of this expired entry, if any
}

// Cache memoizes a Loader per key for a fixed TTL.
type Cache[V any] struct {
	load         Loader[V]
	ttl          time.Duration
	loadTimeout  time.Duration
	errorBackoff time.Duration
	maxEntries   int
	now          func() time.Time

	group singleflight.Group

	mu    sync.Mutex
	items map[string]entry[V]
}

// Option tweaks a Cache.
type Option func(*options)

type options struct {
	loadTimeout  time.Duration
	errorBackoff time.Duration
	maxEntries   int
	now          func() time.Time
}

// WithLoadTimeout bounds one upstream call (default 5s).
func WithLoadTimeout(d time.Duration) Option { return func(o *options) { o.loadTimeout = d } }

// WithErrorBackoff sets how long a stale value is served without retrying
// after a failed refresh (default 30s).
func WithErrorBackoff(d time.Duration) Option { return func(o *options) { o.errorBackoff = d } }

// WithMaxEntries bounds memory for caches keyed by user input (default 1000).
func WithMaxEntries(n int) Option { return func(o *options) { o.maxEntries = n } }

// WithClock replaces time.Now, for tests.
func WithClock(now func() time.Time) Option { return func(o *options) { o.now = now } }

// New returns a Cache that keeps values for ttl.
func New[V any](ttl time.Duration, load Loader[V], opts ...Option) *Cache[V] {
	o := options{loadTimeout: 5 * time.Second, errorBackoff: 30 * time.Second, maxEntries: 1000, now: time.Now}
	for _, opt := range opts {
		opt(&o)
	}
	return &Cache[V]{
		load:         load,
		ttl:          ttl,
		loadTimeout:  o.loadTimeout,
		errorBackoff: o.errorBackoff,
		maxEntries:   max(o.maxEntries, 1),
		now:          o.now,
		items:        make(map[string]entry[V]),
	}
}

// Get returns the cached value for key, loading it if missing or expired.
func (c *Cache[V]) Get(ctx context.Context, key string) (Result[V], error) {
	c.mu.Lock()
	cached, ok := c.items[key]
	c.mu.Unlock()
	if ok {
		now := c.now()
		if now.Sub(cached.fetchedAt) < c.ttl {
			return Result[V]{Value: cached.value, FetchedAt: cached.fetchedAt}, nil
		}
		if !cached.failedAt.IsZero() && now.Sub(cached.failedAt) < c.errorBackoff {
			return Result[V]{Value: cached.value, FetchedAt: cached.fetchedAt, Stale: true}, nil
		}
	}

	// The shared load must not die with whichever caller happened to start it,
	// so it runs on a detached context with its own timeout; each caller still
	// stops waiting when its own ctx is done.
	ch := c.group.DoChan(key, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.loadTimeout)
		defer cancel()
		v, err := c.load(loadCtx, key)
		if err != nil {
			c.markFailed(key)
			return nil, err
		}
		e := entry[V]{value: v, fetchedAt: c.now()}
		c.store(key, e)
		return e, nil
	})

	select {
	case <-ctx.Done():
		return Result[V]{}, ctx.Err()
	case res := <-ch:
		if res.Err == nil {
			e := res.Val.(entry[V]) //nolint:forcetypeassert // only this function puts values in the group
			return Result[V]{Value: e.value, FetchedAt: e.fetchedAt}, nil
		}
		if ok {
			return Result[V]{Value: cached.value, FetchedAt: cached.fetchedAt, Stale: true}, nil
		}
		return Result[V]{}, res.Err
	}
}

func (c *Cache[V]) store(key string, e entry[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists && len(c.items) >= c.maxEntries {
		c.evictLocked()
	}
	c.items[key] = e
}

// markFailed starts the error backoff for an existing entry. Keys that were
// never loaded have nothing stale to serve, so their errors are not remembered.
func (c *Cache[V]) markFailed(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[key]; ok {
		e.failedAt = c.now()
		c.items[key] = e
	}
}

// evictLocked drops expired entries, or the oldest one if none expired.
// O(n), which is fine for the few hundred entries this cache holds.
func (c *Cache[V]) evictLocked() {
	now := c.now()
	var (
		oldestKey string
		oldest    time.Time
		found     bool
	)
	for k, e := range c.items {
		if now.Sub(e.fetchedAt) >= c.ttl {
			delete(c.items, k)
			continue
		}
		if !found || e.fetchedAt.Before(oldest) {
			oldestKey, oldest, found = k, e.fetchedAt, true
		}
	}
	if found && len(c.items) >= c.maxEntries {
		delete(c.items, oldestKey)
	}
}

// Len reports the number of cached keys.
func (c *Cache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
