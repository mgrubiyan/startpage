package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
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
	want := store.Settings{
		SearchEngine: "g", City: "Санкт-Петербург",
		Crypto: []string{"BTC", "ETH", "SOL", "TON"}, Shares: []string{"SBER", "GAZP", "LKOH", "YDEX"},
		Calendars: []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v", got)
	}
	want = store.Settings{
		SearchEngine: "y", City: "Москва", Crypto: []string{"TON", "BTC"}, Shares: []string{"T"},
		Calendars: []string{"https://p52-caldav.icloud.com/published/2/a", "https://example.com/b.ics"},
	}
	if got, err = s.UpdateSettings(ctx, want); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateSettings = %+v, %v", got, err)
	}
	if got, _ = s.Settings(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("after update = %+v", got)
	}
	// An emptied list is an empty array: pgx would send a nil slice as NULL,
	// which the NOT NULL column refuses.
	if got, err = s.UpdateSettings(ctx, store.Settings{SearchEngine: "y", City: "Москва"}); err != nil ||
		len(got.Crypto) != 0 || got.Crypto == nil || len(got.Shares) != 0 || got.Calendars == nil || len(got.Calendars) != 0 {
		t.Fatalf("emptied lists = %+v, %v", got, err)
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

func TestSessions(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	live := bytes32(1)
	expired := bytes32(2)
	if err := s.CreateSession(ctx, live, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, expired, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SessionValid(ctx, live, now); err != nil || !ok {
		t.Fatalf("live session = %v, %v", ok, err)
	}
	if ok, _ := s.SessionValid(ctx, expired, now); ok {
		t.Fatal("expired session reported valid")
	}
	if n, err := s.PruneSessions(ctx, now); err != nil || n != 1 {
		t.Fatalf("PruneSessions = %d, %v", n, err)
	}
	if err := s.DeleteSession(ctx, live); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionValid(ctx, live, now); ok {
		t.Fatal("deleted session reported valid")
	}
	if err := s.CreateSession(ctx, []byte("short"), now.Add(time.Hour)); err == nil {
		t.Fatal("non-SHA-256 token hash must violate CHECK constraint")
	}
}

func TestPasswordHashReplacementEndsSessions(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if h, err := s.PasswordHash(ctx); err != nil || h != nil {
		t.Fatalf("fresh database: hash %q, err %v; want nil, nil", h, err)
	}
	if err := s.CreateSession(ctx, bytes32(1), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, hash := range [][]byte{[]byte("first-hash"), []byte("second-hash")} { // insert, then upsert
		if err := s.ReplacePasswordHash(ctx, hash); err != nil {
			t.Fatalf("ReplacePasswordHash(%q): %v", hash, err)
		}
		if got, err := s.PasswordHash(ctx); err != nil || !bytes.Equal(got, hash) {
			t.Fatalf("PasswordHash = %q, %v; want %q", got, err, hash)
		}
	}
	if ok, _ := s.SessionValid(ctx, bytes32(1), now); ok {
		t.Fatal("session survived a password hash replacement")
	}
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	out[0] = b
	return out
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

func TestNoteRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	n, err := s.Note(ctx)
	if err != nil || n.Text != "" {
		t.Fatalf("fresh note = %+v, %v", n, err)
	}
	saved, err := s.SaveNote(ctx, "строка\n\tвторая")
	if err != nil || saved.Text != "строка\n\tвторая" || !saved.UpdatedAt.After(n.UpdatedAt) {
		t.Fatalf("SaveNote = %+v, %v (was %v)", saved, err, n.UpdatedAt)
	}
	if got, _ := s.Note(ctx); got != saved {
		t.Fatalf("Note = %+v, want %+v", got, saved)
	}
	if _, err := s.SaveNote(ctx, strings.Repeat("я", 10001)); err == nil {
		t.Fatal("an over-long note must violate the CHECK constraint")
	}
}

func TestTasks(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	var ids []int64
	for _, title := range []string{"a", "b", "c", "d"} {
		task, err := s.AddTask(ctx, title)
		if err != nil || task.Title != title || task.Done {
			t.Fatalf("AddTask(%q) = %+v, %v", title, task, err)
		}
		ids = append(ids, task.ID)
	}
	// Finish c, then a: open ones stay in creation order, finished ones go
	// below, the most recently finished first.
	for _, id := range []int64{ids[2], ids[0]} {
		if task, err := s.SetTaskDone(ctx, id, true); err != nil || !task.Done {
			t.Fatalf("SetTaskDone(%d) = %+v, %v", id, task, err)
		}
	}
	order := func() string {
		t.Helper()
		list, err := s.Tasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		for _, task := range list {
			b.WriteString(task.Title)
			if task.Done {
				b.WriteString("✓")
			}
		}
		return b.String()
	}
	if got := order(); got != "bda✓c✓" {
		t.Fatalf("order = %s, want bda✓c✓", got)
	}
	// Marking a done task done again keeps its place.
	if _, err := s.SetTaskDone(ctx, ids[2], true); err != nil {
		t.Fatal(err)
	}
	if got := order(); got != "bda✓c✓" {
		t.Fatalf("repeated done reordered: %s", got)
	}
	if task, err := s.SetTaskDone(ctx, ids[0], false); err != nil || task.Done || task.Title != "a" {
		t.Fatalf("reopen = %+v, %v", task, err)
	}
	if got := order(); got != "abdc✓" {
		t.Fatalf("after reopen = %s, want abdc✓", got)
	}

	if _, err := s.SetTaskDone(ctx, 1<<40, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing task err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTask(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTask(ctx, ids[1]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	if n, err := s.DeleteDoneTasks(ctx); err != nil || n != 1 {
		t.Fatalf("DeleteDoneTasks = %d, %v", n, err)
	}
	if got := order(); got != "ad" {
		t.Fatalf("after clearing = %s, want ad", got)
	}

	if _, err := s.AddTask(ctx, ""); err == nil {
		t.Fatal("an empty title must violate the CHECK constraint")
	}
	if _, err := s.AddTask(ctx, strings.Repeat("я", 201)); err == nil {
		t.Fatal("an over-long title must violate the CHECK constraint")
	}
}

func TestCheckHistory(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := context.Background()

	a, err := s.AddService(ctx, store.Service{Name: "a", URL: "https://a.example"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddService(ctx, store.Service{Name: "b", URL: "https://b.example"})
	if err != nil {
		t.Fatal(err)
	}
	h := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	checks := []store.Check{
		{ServiceID: a.ID, CheckedAt: h.Add(-time.Minute), OK: true, Latency: time.Second}, // before since: left out
		{ServiceID: a.ID, CheckedAt: h.Add(5 * time.Minute), OK: true, Latency: 100 * time.Millisecond},
		{ServiceID: a.ID, CheckedAt: h.Add(59 * time.Minute), OK: true, Latency: 201 * time.Millisecond},
		// A timeout says nothing about how fast the service answers.
		{ServiceID: a.ID, CheckedAt: h.Add(30 * time.Minute), OK: false, Latency: 10 * time.Second},
		{ServiceID: a.ID, CheckedAt: h.Add(2 * time.Hour), OK: false, Latency: 10 * time.Second},
		{ServiceID: b.ID, CheckedAt: h.Add(time.Hour), OK: true, Latency: 7 * time.Millisecond},
	}
	if err := s.RecordChecks(ctx, checks); err != nil {
		t.Fatal(err)
	}

	got, err := s.CheckHistory(ctx, h, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64][]store.CheckBucket{
		a.ID: {
			{Start: h, Total: 3, OK: 2, AvgLatency: 151 * time.Millisecond}, // 150.5 rounds to 151
			{Start: h.Add(2 * time.Hour), Total: 1, OK: 0, AvgLatency: 0},
		},
		b.ID: {{Start: h.Add(time.Hour), Total: 1, OK: 1, AvgLatency: 7 * time.Millisecond}},
	}
	if len(got) != len(want) {
		t.Fatalf("history for %d services, want %d: %+v", len(got), len(want), got)
	}
	for id, buckets := range want {
		if len(got[id]) != len(buckets) {
			t.Fatalf("service %d: %+v, want %+v", id, got[id], buckets)
		}
		for i, w := range buckets {
			g := got[id][i]
			if !g.Start.Equal(w.Start) || g.Total != w.Total || g.OK != w.OK || g.AvgLatency != w.AvgLatency {
				t.Errorf("service %d slot %d = %+v, want %+v", id, i, g, w)
			}
		}
	}
}
