package postgres

import (
	"context"
	"fmt"
	"time"
)

// CreateSession stores a session token hash.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx,
		"INSERT INTO sessions (token_hash, expires_at) VALUES ($1, $2)", tokenHash, expiresAt); err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// SessionValid reports whether a non-expired session with this token hash exists.
func (s *Store) SessionValid(ctx context.Context, tokenHash []byte, now time.Time) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM sessions WHERE token_hash = $1 AND expires_at > $2)", tokenHash, now).
		Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("check session: %w", err)
	}
	return ok, nil
}

// DeleteSession removes a session; deleting an unknown session is not an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash = $1", tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= $1", now)
	if err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
