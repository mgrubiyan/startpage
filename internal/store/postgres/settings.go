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

// UpdateSettings applies a patch to the settings row and returns the stored
// values. A field left nil is sent as NULL and COALESCE keeps the column, all
// in one statement: no read-then-write for a concurrent save to slip into.
func (s *Store) UpdateSettings(ctx context.Context, p store.SettingsPatch) (store.Settings, error) {
	var out store.Settings
	err := s.pool.QueryRow(ctx, `
		UPDATE settings SET
			search_engine = COALESCE($1, search_engine),
			city = COALESCE($2, city),
			crypto = COALESCE($3, crypto),
			shares = COALESCE($4, shares),
			calendars = COALESCE($5, calendars),
			updated_at = now()
		WHERE id = 1
		RETURNING search_engine, city, crypto, shares, calendars`,
		p.SearchEngine, p.City, list(p.Crypto), list(p.Shares), list(p.Calendars)).
		Scan(&out.SearchEngine, &out.City, &out.Crypto, &out.Shares, &out.Calendars)
	if err != nil {
		return store.Settings{}, fmt.Errorf("update settings: %w", err)
	}
	return out, nil
}

// list is NULL for a list left out of a patch, and an array otherwise, even
// an empty one: pgx would send a nil slice as NULL, which would keep the column.
func list(p *[]string) any {
	if p == nil {
		return nil
	}
	if *p == nil {
		return []string{}
	}
	return *p
}
