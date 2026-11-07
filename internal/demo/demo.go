// Package demo prepares the data a public, read-only showcase instance starts with.
package demo

import (
	"context"
	"fmt"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Store is the part of the persistence layer seeding needs; *postgres.Store implements it.
type Store interface {
	Services(ctx context.Context) ([]store.Service, error)
	AddService(ctx context.Context, s store.Service) (store.Service, error)
}

// Services are the monitor targets of a fresh demo. They mix this instance's own
// health check, the upstream APIs the page itself depends on, two public
// endpoints, and one address that is always down, so the red state shows too.
// selfURL is the base URL the server is reachable on from inside its container.
func Services(selfURL string) []store.Service {
	return []store.Service{
		{Name: "Startpage API", URL: selfURL + "/healthz"},
		{Name: "Open-Meteo (погода)", URL: "https://api.open-meteo.com/v1/forecast?latitude=59.94&longitude=30.31&current=temperature_2m"},
		{Name: "Курсы ЦБ РФ", URL: "https://www.cbr-xml-daily.ru/daily_json.js"},
		{Name: "Go module proxy", URL: "https://proxy.golang.org/"},
		{Name: "GitHub Status", URL: "https://www.githubstatus.com/api/v2/status.json"},
		// Port 9 (discard) has no listener, so the probe fails with "connection refused".
		{Name: "Старый бот (выключен)", URL: "http://127.0.0.1:9/health"},
	}
}

// Seed adds the demo services when none exist yet, so a restart doesn't duplicate
// them. It is meant for one instance per database: two starting at the same moment
// on an empty database could both seed. Bookmarks need no seeding: the first
// migration adds them.
func Seed(ctx context.Context, s Store, selfURL string) error {
	existing, err := s.Services(ctx)
	if err != nil {
		return fmt.Errorf("demo seed: list services: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, svc := range Services(selfURL) {
		if _, err := s.AddService(ctx, svc); err != nil {
			return fmt.Errorf("demo seed: add %q: %w", svc.Name, err)
		}
	}
	return nil
}
