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
