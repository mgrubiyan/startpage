// Package store defines the persistent domain types shared by the HTTP layer,
// the monitor and the storage implementation.
package store

import (
	"errors"
	"time"
)

// ErrNotFound is returned when a row addressed by ID does not exist.
var ErrNotFound = errors.New("not found")

// Settings is the single-row user configuration.
type Settings struct {
	SearchEngine string
	City         string
}

// Bookmark is a tile on the start page.
type Bookmark struct {
	ID    int64
	Title string
	URL   string
}

// Service is an HTTP endpoint the monitor checks periodically.
type Service struct {
	ID   int64
	Name string
	URL  string
}

// Check is the outcome of one probe of a service.
type Check struct {
	ServiceID  int64
	CheckedAt  time.Time
	OK         bool
	StatusCode int // 0 when no HTTP response was received
	Latency    time.Duration
	Error      string
}

// ServiceStatus is a service with its latest check and uptime over a window.
type ServiceStatus struct {
	Service
	Last        *Check // nil until the first check
	TotalChecks int
	OKChecks    int
}

// Uptime returns the share of successful checks in percent, or false if there were none.
func (s ServiceStatus) Uptime() (float64, bool) {
	if s.TotalChecks == 0 {
		return 0, false
	}
	return 100 * float64(s.OKChecks) / float64(s.TotalChecks), true
}
