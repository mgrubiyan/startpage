package art

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrUnavailable is returned while no painting has loaded yet and the last
// attempt failed recently.
var ErrUnavailable = errors.New("art: no painting available yet")

// PickFunc chooses the painting for a day; (*Client).Pick is one.
type PickFunc func(ctx context.Context, day string) (Artwork, error)

// Daily holds the painting of the current UTC day and loads it once a day.
//
// Once a painting is held, nobody waits for a download: on a new day the
// previous painting is served while the next one loads in the background. If
// a load fails, it is retried only after a backoff, so a museum outage neither
// blanks the background nor sends every request to the museum. Concurrent
// loads of a day are collapsed.
type Daily struct {
	pick    PickFunc
	now     func() time.Time
	timeout time.Duration
	backoff time.Duration

	group singleflight.Group

	mu       sync.Mutex
	cur      Artwork
	have     bool
	day      string
	failedAt time.Time
}

// NewDaily returns a Daily that loads with pick.
func NewDaily(pick PickFunc) *Daily {
	return &Daily{pick: pick, now: time.Now, timeout: 2 * time.Minute, backoff: 10 * time.Minute}
}

// Today returns today's painting, or the last one that loaded while today's
// is loading or can't be had. It blocks only while no painting has loaded yet.
func (d *Daily) Today(ctx context.Context) (Artwork, error) {
	day := d.now().UTC().Format(time.DateOnly)

	d.mu.Lock()
	cur, have, curDay, failedAt := d.cur, d.have, d.day, d.failedAt
	d.mu.Unlock()
	if have && curDay == day {
		return cur, nil
	}
	backingOff := !failedAt.IsZero() && d.now().Sub(failedAt) < d.backoff
	if have {
		if !backingOff {
			d.load(ctx, day) // in the background; the result lands in d.cur
		}
		return cur, nil
	}
	if backingOff {
		return Artwork{}, ErrUnavailable
	}

	select {
	case <-ctx.Done():
		return Artwork{}, ctx.Err()
	case res := <-d.load(ctx, day):
		if res.Err != nil {
			return Artwork{}, res.Err
		}
		return res.Val.(Artwork), nil //nolint:forcetypeassert // only load puts values in the group
	}
}

// load starts (or joins) the load of day. It runs detached from the caller,
// like internal/cache: a closed tab must not cancel a download others share.
func (d *Daily) load(ctx context.Context, day string) <-chan singleflight.Result {
	return d.group.DoChan(day, func() (any, error) {
		d.mu.Lock()
		if d.have && d.day == day { // another load finished since our read
			a := d.cur
			d.mu.Unlock()
			return a, nil
		}
		d.mu.Unlock()

		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeout)
		defer cancel()
		a, err := d.pick(loadCtx, day)

		d.mu.Lock()
		defer d.mu.Unlock()
		if err != nil {
			d.failedAt = d.now()
			return nil, err
		}
		// Loads of two days can overlap around midnight; the older one must
		// not replace the newer painting when it finishes last.
		if !d.have || day >= d.day {
			d.cur, d.have, d.day, d.failedAt = a, true, day, time.Time{}
		}
		return a, nil
	})
}
