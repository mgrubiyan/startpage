package auth

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memSessions struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	pruned   int
}

func newMemSessions() *memSessions { return &memSessions{sessions: map[string]time.Time{}} }

func (m *memSessions) CreateSession(_ context.Context, h []byte, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(h)] = exp
	return nil
}

func (m *memSessions) SessionValid(_ context.Context, h []byte, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	exp, ok := m.sessions[string(h)]
	return ok && now.Before(exp), nil
}

func (m *memSessions) DeleteSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(h))
	return nil
}

func (m *memSessions) PruneSessions(context.Context, time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned++
	return 0, nil
}

func newService(t *testing.T) (*Service, *memSessions, *time.Time) {
	t.Helper()
	store := newMemSessions()
	s, err := New("correct-horse", store, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, store, &now
}

func TestLoginAndAuthenticate(t *testing.T) {
	s, store, now := newService(t)
	ctx := context.Background()

	if _, _, err := s.Login(ctx, "wrong"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("wrong password err = %v", err)
	}
	token, exp, err := s.Login(ctx, "correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires = %v", exp)
	}
	if ok, _ := s.Authenticate(ctx, token); !ok {
		t.Fatal("fresh token rejected")
	}

	// The raw token must never reach the store, only its hash.
	for h := range store.sessions {
		if bytes.Contains([]byte(h), []byte(token)) {
			t.Fatal("raw token persisted")
		}
	}
	if store.pruned != 1 {
		t.Fatal("expired sessions not pruned on login")
	}

	*now = now.Add(time.Hour)
	if ok, _ := s.Authenticate(ctx, token); ok {
		t.Fatal("expired token accepted")
	}
}

func TestTokensAreUnique(t *testing.T) {
	s, _, _ := newService(t)
	a, _, _ := s.Login(context.Background(), "correct-horse")
	b, _, _ := s.Login(context.Background(), "correct-horse")
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q / %q: want distinct 32-byte base64url values", a, b)
	}
}

func TestMalformedTokensRejected(t *testing.T) {
	s, _, _ := newService(t)
	for _, tok := range []string{"", "short", "!!!not-base64!!!", string(make([]byte, 43))} {
		if ok, err := s.Authenticate(context.Background(), tok); ok || err != nil {
			t.Fatalf("Authenticate(%q) = %v, %v", tok, ok, err)
		}
	}
}

func TestLogout(t *testing.T) {
	s, _, _ := newService(t)
	ctx := context.Background()
	token, _, _ := s.Login(ctx, "correct-horse")
	if err := s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Authenticate(ctx, token); ok {
		t.Fatal("token valid after logout")
	}
	if err := s.Logout(ctx, "garbage"); err != nil {
		t.Fatalf("logout with garbage token: %v", err)
	}
}
