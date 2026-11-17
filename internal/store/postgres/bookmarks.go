package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Bookmarks returns all bookmarks in display order.
func (s *Store) Bookmarks(ctx context.Context) ([]store.Bookmark, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, title, url FROM bookmarks ORDER BY position, id")
	if err != nil {
		return nil, fmt.Errorf("select bookmarks: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[store.Bookmark])
	if err != nil {
		return nil, fmt.Errorf("select bookmarks: %w", err)
	}
	return out, nil
}

// AddBookmark appends a bookmark to the end of the list.
func (s *Store) AddBookmark(ctx context.Context, b store.Bookmark) (store.Bookmark, error) {
	var out store.Bookmark
	// Two concurrent inserts may get the same position; ordering then falls back to id,
	// which still keeps "newest last" without needing a lock.
	err := s.pool.QueryRow(ctx, `
		INSERT INTO bookmarks (title, url, position)
		VALUES ($1, $2, (SELECT COALESCE(MAX(position) + 1, 0) FROM bookmarks))
		RETURNING id, title, url`, b.Title, b.URL).
		Scan(&out.ID, &out.Title, &out.URL)
	if err != nil {
		return store.Bookmark{}, fmt.Errorf("insert bookmark: %w", err)
	}
	return out, nil
}

// ReorderBookmarks puts the bookmarks in the order of ids, which must name
// every bookmark exactly once: a list read before another tab added or removed
// one gets ErrConflict, rather than leaving the new one wherever it falls.
func (s *Store) ReorderBookmarks(ctx context.Context, ids []int64) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// FOR UPDATE: a concurrent reorder waits for this one instead of mixing with it.
		rows, err := tx.Query(ctx, "SELECT id FROM bookmarks ORDER BY id FOR UPDATE")
		if err != nil {
			return fmt.Errorf("lock bookmarks: %w", err)
		}
		have, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return fmt.Errorf("lock bookmarks: %w", err)
		}
		if !sameIDs(have, ids) {
			return store.ErrConflict
		}
		_, err = tx.Exec(ctx, `
			UPDATE bookmarks AS b SET position = o.n
			FROM unnest($1::bigint[]) WITH ORDINALITY AS o(id, n)
			WHERE b.id = o.id`, ids)
		if err != nil {
			return fmt.Errorf("reorder bookmarks: %w", err)
		}
		return nil
	})
}

// sameIDs reports whether ids holds exactly the ids in have (sorted), once each.
func sameIDs(have, ids []int64) bool {
	if len(have) != len(ids) {
		return false
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return false
		}
		seen[id] = true
	}
	for _, id := range have {
		if !seen[id] {
			return false
		}
	}
	return true
}

// DeleteBookmark removes a bookmark by ID.
func (s *Store) DeleteBookmark(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM bookmarks WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
