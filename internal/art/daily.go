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
// If a load fails, the previous painting stays up and the load is retried
// only after a backoff, so a museum outage neither blanks the background nor
// makes every request wait on the timeout. Concurrent loads are collapsed.
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
	return &Daily{pick: pick, now: time.Now, timeout: time.Minute, backoff: 10 * time.Minute}
}

// Today returns today's painting, or the last one that loaded if today's can't be had.
func (d *Daily) Today(ctx context.Context) (Artwork, error) {
	day := d.now().UTC().Format(time.DateOnly)

	d.mu.Lock()
	cur, have, curDay, failedAt := d.cur, d.have, d.day, d.failedAt
	d.mu.Unlock()
	if have && curDay == day {
		return cur, nil
	}
	if !failedAt.IsZero() && d.now().Sub(failedAt) < d.backoff {
		if have {
			return cur, nil
		}
		return Artwork{}, ErrUnavailable
	}

	// Detached from the caller, like internal/cache: a closed tab must not
	// cancel the download that everyone else is waiting for.
	ch := d.group.DoChan(day, func() (any, error) {
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
		d.cur, d.have, d.day, d.failedAt = a, true, day, time.Time{}
		return a, nil
	})

	select {
	case <-ctx.Done():
		return Artwork{}, ctx.Err()
	case res := <-ch:
		if res.Err == nil {
			return res.Val.(Artwork), nil //nolint:forcetypeassert // only this function puts values in the group
		}
		if have {
			return cur, nil
		}
		return Artwork{}, res.Err
	}
}
