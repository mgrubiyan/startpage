package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary application-wide key for pg_advisory_lock,
// so that two instances starting at once do not apply migrations concurrently.
const migrationLockID int64 = 0x53545047 // "STPG"

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	seen := map[int]string{}
	migs := make([]migration, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %q: name must start with a positive number and underscore", name)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", prev, name, version)
		}
		seen[version] = name
		body, err := fs.ReadFile(fsys, "migrations/"+name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		migs = append(migs, migration{version: version, name: name, sql: string(body)})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].version < migs[j].version })
	return migs, nil
}

// Migrate applies pending migrations, each in its own transaction.
//
// The advisory lock is session-scoped, so lock, migrations and unlock must all run
// on one dedicated connection; going through the pool could unlock on a different session.
func (s *Store) Migrate(ctx context.Context) error {
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}

	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer pooled.Release()
	conn := pooled.Conn()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Unlock even if ctx was cancelled. If unlocking fails, close the connection:
		// ending the session is the only other way to release the lock.
		bg := context.WithoutCancel(ctx)
		if _, err := conn.Exec(bg, "SELECT pg_advisory_unlock($1)", migrationLockID); err != nil {
			_ = conn.Close(bg)
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		name       text        NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	applied := make(map[int]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgx.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migration %s: begin: %w", m.name, err)
	}
	defer rollback(ctx, tx)

	if _, err := tx.Exec(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.version, m.name); err != nil {
		return fmt.Errorf("migration %s: record version: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migration %s: commit: %w", m.name, err)
	}
	return nil
}
