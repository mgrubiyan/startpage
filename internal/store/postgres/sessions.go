package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// PasswordHash returns the stored bcrypt hash of the login password, or nil if
// none has been stored yet.
func (s *Store) PasswordHash(ctx context.Context) ([]byte, error) {
	var hash []byte
	err := s.pool.QueryRow(ctx, "SELECT password_hash FROM auth WHERE id = 1").Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read password hash: %w", err)
	}
	return hash, nil
}

// ReplacePasswordHash stores a new password hash and deletes every session in one
// transaction, so no session outlives the password it was issued under.
func (s *Store) ReplacePasswordHash(ctx context.Context, hash []byte) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM sessions"); err != nil {
			return fmt.Errorf("delete sessions: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth (id, password_hash) VALUES (1, $1)
			ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash, updated_at = now()`,
			hash); err != nil {
			return fmt.Errorf("store password hash: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace password hash: %w", err)
	}
	return nil
}
