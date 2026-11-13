package postgres

import (
	"context"
	"fmt"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Settings returns the single settings row created by the first migration.
func (s *Store) Settings(ctx context.Context) (store.Settings, error) {
	var out store.Settings
	err := s.pool.QueryRow(ctx, "SELECT search_engine, city, crypto, shares, calendars FROM settings WHERE id = 1").
		Scan(&out.SearchEngine, &out.City, &out.Crypto, &out.Shares, &out.Calendars)
	if err != nil {
		return store.Settings{}, fmt.Errorf("select settings: %w", err)
	}
	return out, nil
}

// UpdateSettings overwrites the settings row and returns the stored values.
func (s *Store) UpdateSettings(ctx context.Context, in store.Settings) (store.Settings, error) {
	var out store.Settings
	err := s.pool.QueryRow(ctx, `
		UPDATE settings SET search_engine = $1, city = $2, crypto = $3, shares = $4, calendars = $5, updated_at = now()
		WHERE id = 1
		RETURNING search_engine, city, crypto, shares, calendars`,
		in.SearchEngine, in.City, nonNil(in.Crypto), nonNil(in.Shares), nonNil(in.Calendars)).
		Scan(&out.SearchEngine, &out.City, &out.Crypto, &out.Shares, &out.Calendars)
	if err != nil {
		return store.Settings{}, fmt.Errorf("update settings: %w", err)
	}
	return out, nil
}

// nonNil makes an empty list an empty array: pgx sends a nil slice as NULL.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
