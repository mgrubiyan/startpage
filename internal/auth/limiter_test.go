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

func TestLimiterFailsClosedWhenFull(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l := NewLimiter(5, time.Minute)
	l.now = func() time.Time { return now }
	for i := range maxTrackedKeys {
		l.Allow(strconv.Itoa(i))
	}
	now = now.Add(30 * time.Second) // every bucket still live: nothing to sweep
	if l.Allow("rotated-ip") {
		t.Fatal("new key admitted while the table is full of live buckets")
	}
	if len(l.hits) != maxTrackedKeys {
		t.Fatalf("map grew past the cap: %d", len(l.hits))
	}
	if !l.Allow("0") {
		t.Fatal("an already-tracked client under its limit must still be allowed")
	}
}
