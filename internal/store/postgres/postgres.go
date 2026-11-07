// Package postgres implements persistence on PostgreSQL via pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is a PostgreSQL-backed repository for all startpage data.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to databaseURL and waits until the database answers or ctx expires.
// Waiting makes `docker compose up` robust when the app starts before Postgres is ready.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	for {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return &Store{pool: pool}, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("database not reachable: %w", errors.Join(err, ctx.Err()))
		case <-time.After(time.Second):
		}
	}
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }

// rollback is deferred after Begin; it is a no-op once the transaction is committed.
func rollback(ctx context.Context, tx pgx.Tx) {
	_ = tx.Rollback(context.WithoutCancel(ctx)) // ErrTxClosed after a successful commit is expected
}
