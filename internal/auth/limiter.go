package auth

import (
	"sync"
	"time"
)

// Limiter is a fixed-window rate limiter keyed by client (e.g. IP address),
// used to slow down password guessing.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string]bucket
}

type bucket struct {
	count int
	start time.Time
}

// maxTrackedKeys bounds memory if many distinct clients hit the endpoint.
const maxTrackedKeys = 10_000

// NewLimiter allows max attempts per key per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, hits: make(map[string]bucket)}
}

// Allow records an attempt for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.hits[key]
	if !ok || now.Sub(b.start) >= l.window {
		if !ok && len(l.hits) >= maxTrackedKeys {
			l.sweepLocked(now)
			// Still full: thousands of distinct clients inside one window is an
			// attack (e.g. rotating X-Forwarded-For), so fail closed for new keys
			// rather than grow without bound.
			if len(l.hits) >= maxTrackedKeys {
				return false
			}
		}
		l.hits[key] = bucket{count: 1, start: now}
		return true
	}
	if b.count >= l.max {
		return false
	}
	b.count++
	l.hits[key] = b
	return true
}

func (l *Limiter) sweepLocked(now time.Time) {
	for k, b := range l.hits {
		if now.Sub(b.start) >= l.window {
			delete(l.hits, k)
		}
	}
}
