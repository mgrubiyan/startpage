package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mgrubiyan/startpage/internal/store"
)

// newTestStore returns a migrated Store living in its own throwaway schema,
// so tests can run in parallel against one database without seeing each other's rows.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	schema := "test_" + hex.EncodeToString(buf)

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(context.Background(), "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(migs) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(migs))
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files fstest.MapFS
		ok    bool
	}{
		{"sorted by number", fstest.MapFS{
			"migrations/010_b.sql": {Data: []byte("b")},
			"migrations/002_a.sql": {Data: []byte("a")},
		}, true},
		{"duplicate version", fstest.MapFS{
			"migrations/001_a.sql":  {Data: []byte("a")},
			"migrations/0001_b.sql": {Data: []byte("b")},
		}, false},
		{"no numeric prefix", fstest.MapFS{"migrations/init.sql": {Data: []byte("x")}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migs, err := loadMigrations(tt.files)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && (migs[0].version != 2 || migs[1].version != 10) {
				t.Fatalf("not sorted: %+v", migs)
			}
		})
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SearchEngine != "g" || got.City != "Санкт-Петербург" {
		t.Fatalf("defaults = %+v", got)
	}
	want := store.Settings{SearchEngine: "y", City: "Москва"}
	if got, err = s.UpdateSettings(ctx, want); err != nil || got != want {
		t.Fatalf("UpdateSettings = %+v, %v", got, err)
	}
	if got, _ = s.Settings(ctx); got != want {
		t.Fatalf("after update = %+v", got)
	}
}

func TestBookmarksCRUD(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	seeded, err := s.Bookmarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != 6 || seeded[0].Title != "GitHub" {
		t.Fatalf("seed = %+v", seeded)
	}

	added, err := s.AddBookmark(ctx, store.Bookmark{Title: "Hostkey", URL: "https://hostkey.ru"})
	if err != nil {
		t.Fatal(err)
	}
	all, err := s.Bookmarks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last := all[len(all)-1]; last != added {
		t.Fatalf("new bookmark not last: %+v", all)
	}

	if err := s.DeleteBookmark(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBookmark(ctx, added.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}

	if _, err := s.AddBookmark(ctx, store.Bookmark{Title: "", URL: "https://x.y"}); err == nil {
		t.Fatal("empty title must violate CHECK constraint")
	}
}

func TestServiceStatusesAndRetention(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	up, err := s.AddService(ctx, store.Service{Name: "up", URL: "https://up.example"})
	if err != nil {
		t.Fatal(err)
	}
	never, err := s.AddService(ctx, store.Service{Name: "never", URL: "https://never.example"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	checks := []store.Check{
		{ServiceID: up.ID, CheckedAt: now.Add(-48 * time.Hour), OK: false, Latency: time.Second, Error: "old"},
		{ServiceID: up.ID, CheckedAt: now.Add(-2 * time.Minute), OK: false, Latency: 6 * time.Second, Error: "timeout"},
		{ServiceID: up.ID, CheckedAt: now.Add(-time.Minute), OK: true, StatusCode: 200, Latency: 42 * time.Millisecond},
		{ServiceID: up.ID, CheckedAt: now, OK: true, StatusCode: 204, Latency: 40 * time.Millisecond},
		{ServiceID: 999_999, CheckedAt: now, OK: true}, // deleted service: silently skipped
	}
	if err := s.RecordChecks(ctx, checks); err != nil {
		t.Fatalf("RecordChecks: %v", err)
	}
	if err := s.RecordChecks(ctx, checks[:1]); err != nil {
		t.Fatalf("duplicate RecordChecks must be ignored, got %v", err)
	}

	statuses, err := s.ServiceStatuses(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 {
		t.Fatalf("got %d statuses", len(statuses))
	}
	st := statuses[0]
	if st.ID != up.ID || st.Last == nil || !st.Last.OK || st.Last.StatusCode != 204 ||
		st.Last.Latency != 40*time.Millisecond || !st.Last.CheckedAt.Equal(now) {
		t.Fatalf("latest check wrong: %+v / %+v", st, st.Last)
	}
	if st.TotalChecks != 3 || st.OKChecks != 2 {
		t.Fatalf("window counts = %d/%d, want 2 ok of 3 (48h-old check excluded)", st.OKChecks, st.TotalChecks)
	}
	if statuses[1].ID != never.ID || statuses[1].Last != nil {
		t.Fatalf("unchecked service: %+v", statuses[1])
	}
	if _, ok := statuses[1].Uptime(); ok {
		t.Fatal("uptime must be unknown without checks")
	}

	pruned, err := s.PruneChecks(ctx, now.Add(-24*time.Hour))
	if err != nil || pruned != 1 {
		t.Fatalf("PruneChecks = %d, %v; want 1", pruned, err)
	}

	if err := s.DeleteService(ctx, up.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM service_checks").Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("checks not cascaded on delete: %d left", left)
	}
}
