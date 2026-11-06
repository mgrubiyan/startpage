package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestLimiterWindow(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(3, time.Minute)
	l.now = func() time.Time { return now }

	for i := range 3 {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("attempt %d rejected", i+1)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("4th attempt in window allowed")
	}
	if !l.Allow("5.6.7.8") {
		t.Fatal("other client throttled by someone else's attempts")
	}

	now = now.Add(time.Minute)
	if !l.Allow("1.2.3.4") {
		t.Fatal("not reset after window")
	}
}

func TestLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(1, time.Minute)
	l.now = func() time.Time { return now }
	for i := range maxTrackedKeys {
		l.Allow(strconv.Itoa(i))
	}
	now = now.Add(time.Minute)
	l.Allow("new")
	if len(l.hits) != 1 {
		t.Fatalf("expired keys not swept: %d tracked", len(l.hits))
	}
}
