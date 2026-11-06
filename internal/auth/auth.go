// Package auth implements single-user password login with server-side sessions.
//
// The password is bcrypt-hashed at startup and never stored in plain text.
// Session tokens are 256-bit random values; only their SHA-256 is persisted,
// so a database leak does not yield usable cookies.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidPassword is returned by Login for a wrong password.
var ErrInvalidPassword = errors.New("invalid password")

// SessionStore persists session token hashes; *postgres.Store implements it.
type SessionStore interface {
	CreateSession(ctx context.Context, tokenHash []byte, expiresAt time.Time) error
	SessionValid(ctx context.Context, tokenHash []byte, now time.Time) (bool, error)
	DeleteSession(ctx context.Context, tokenHash []byte) error
	PruneSessions(ctx context.Context, now time.Time) (int64, error)
}

// Service checks passwords and manages sessions.
type Service struct {
	passwordHash []byte
	sessions     SessionStore
	ttl          time.Duration
	now          func() time.Time
}

const tokenBytes = 32

// New hashes password with bcrypt. The plain password is not retained.
func New(password string, sessions SessionStore, ttl time.Duration) (*Service, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	return &Service{passwordHash: hash, sessions: sessions, ttl: ttl, now: time.Now}, nil
}

// Login verifies password and starts a session, returning the cookie token and its expiry.
func (s *Service) Login(ctx context.Context, password string) (token string, expires time.Time, err error) {
	if err := bcrypt.CompareHashAndPassword(s.passwordHash, []byte(password)); err != nil {
		return "", time.Time{}, ErrInvalidPassword
	}

	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires = now.Add(s.ttl)
	if err := s.sessions.CreateSession(ctx, hashToken(token), expires); err != nil {
		return "", time.Time{}, err
	}
	// Opportunistic cleanup instead of a dedicated goroutine; logins are rare.
	_, _ = s.sessions.PruneSessions(ctx, now)
	return token, expires, nil
}

// Authenticate reports whether token belongs to a live session.
func (s *Service) Authenticate(ctx context.Context, token string) (bool, error) {
	if !wellFormed(token) {
		return false, nil
	}
	return s.sessions.SessionValid(ctx, hashToken(token), s.now())
}

// Logout ends the session for token. Unknown tokens are not an error.
func (s *Service) Logout(ctx context.Context, token string) error {
	if !wellFormed(token) {
		return nil
	}
	return s.sessions.DeleteSession(ctx, hashToken(token))
}

func wellFormed(token string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(raw) == tokenBytes
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
