package postgres

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Services returns all monitored services.
func (s *Store) Services(ctx context.Context) ([]store.Service, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, name, url FROM services ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("select services: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[store.Service])
	if err != nil {
		return nil, fmt.Errorf("select services: %w", err)
	}
	return out, nil
}

// AddService registers a new service to monitor.
func (s *Store) AddService(ctx context.Context, svc store.Service) (store.Service, error) {
	var out store.Service
	err := s.pool.QueryRow(ctx,
		"INSERT INTO services (name, url) VALUES ($1, $2) RETURNING id, name, url", svc.Name, svc.URL).
		Scan(&out.ID, &out.Name, &out.URL)
	if err != nil {
		return store.Service{}, fmt.Errorf("insert service: %w", err)
	}
	return out, nil
}

// DeleteService removes a service and, via ON DELETE CASCADE, its check history.
func (s *Store) DeleteService(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM services WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RecordChecks stores a batch of check results in one round trip.
// Results for services deleted while the check was in flight are skipped.
func (s *Store) RecordChecks(ctx context.Context, checks []store.Check) error {
	if len(checks) == 0 {
		return nil
	}
	var (
		ids       = make([]int64, len(checks))
		times     = make([]time.Time, len(checks))
		oks       = make([]bool, len(checks))
		codes     = make([]*int32, len(checks))
		latencies = make([]int32, len(checks))
		errs      = make([]*string, len(checks))
	)
	for i, c := range checks {
		ids[i], times[i], oks[i] = c.ServiceID, c.CheckedAt, c.OK
		latencies[i] = int32(min(max(c.Latency.Milliseconds(), 0), math.MaxInt32)) //nolint:gosec // clamped to int32 range
		if c.StatusCode != 0 {
			code := int32(c.StatusCode) //nolint:gosec // HTTP status codes have three digits
			codes[i] = &code
		}
		if c.Error != "" {
			msg := c.Error
			errs[i] = &msg
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO service_checks (service_id, checked_at, ok, status_code, latency_ms, error)
		SELECT c.service_id, c.checked_at, c.ok, c.status_code, c.latency_ms, c.error
		FROM unnest($1::bigint[], $2::timestamptz[], $3::boolean[], $4::integer[], $5::integer[], $6::text[])
			AS c(service_id, checked_at, ok, status_code, latency_ms, error)
		-- FOR KEY SHARE: a service deleted mid-round is skipped instead of failing
		-- the whole batch on the foreign key.
		WHERE EXISTS (SELECT 1 FROM services s WHERE s.id = c.service_id FOR KEY SHARE)
		ON CONFLICT (service_id, checked_at) DO NOTHING`,
		ids, times, oks, codes, latencies, errs)
	if err != nil {
		return fmt.Errorf("insert checks: %w", err)
	}
	return nil
}

// PruneChecks deletes check history older than before and reports how many rows went.
func (s *Store) PruneChecks(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM service_checks WHERE checked_at < $1", before)
	if err != nil {
		return 0, fmt.Errorf("prune checks: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ServiceStatuses returns every service with its latest check and the
// number of total/successful checks since the given time.
func (s *Store) ServiceStatuses(ctx context.Context, since time.Time) ([]store.ServiceStatus, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.name, s.url,
		       last.checked_at, last.ok, last.status_code, last.latency_ms, last.error,
		       agg.total, agg.ok_count
		FROM services s
		LEFT JOIN LATERAL (
			SELECT c.checked_at, c.ok, c.status_code, c.latency_ms, c.error
			FROM service_checks c
			WHERE c.service_id = s.id
			ORDER BY c.checked_at DESC
			LIMIT 1
		) last ON true
		CROSS JOIN LATERAL (
			SELECT count(*) AS total, count(*) FILTER (WHERE c.ok) AS ok_count
			FROM service_checks c
			WHERE c.service_id = s.id AND c.checked_at >= $1
		) agg
		ORDER BY s.id`, since)
	if err != nil {
		return nil, fmt.Errorf("select service statuses: %w", err)
	}
	defer rows.Close()

	var out []store.ServiceStatus
	for rows.Next() {
		var (
			st             store.ServiceStatus
			checkedAt      *time.Time
			ok             *bool
			code           *int32
			latencyMs      *int32
			errMsg         *string
			total, okCount int64
		)
		if err := rows.Scan(&st.ID, &st.Name, &st.URL,
			&checkedAt, &ok, &code, &latencyMs, &errMsg, &total, &okCount); err != nil {
			return nil, fmt.Errorf("scan service status: %w", err)
		}
		if checkedAt != nil && ok != nil {
			last := store.Check{ServiceID: st.ID, CheckedAt: *checkedAt, OK: *ok}
			if code != nil {
				last.StatusCode = int(*code)
			}
			if latencyMs != nil {
				last.Latency = time.Duration(*latencyMs) * time.Millisecond
			}
			if errMsg != nil {
				last.Error = *errMsg
			}
			st.Last = &last
		}
		st.TotalChecks, st.OKChecks = int(total), int(okCount)
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("select service statuses: %w", err)
	}
	return out, nil
}
