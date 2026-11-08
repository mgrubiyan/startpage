package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Note returns the scratchpad created by migration 005.
func (s *Store) Note(ctx context.Context) (store.Note, error) {
	var out store.Note
	err := s.pool.QueryRow(ctx, "SELECT body, updated_at FROM notes WHERE id = 1").
		Scan(&out.Text, &out.UpdatedAt)
	if err != nil {
		return store.Note{}, fmt.Errorf("select note: %w", err)
	}
	return out, nil
}

// SaveNote overwrites the scratchpad. The last save wins.
func (s *Store) SaveNote(ctx context.Context, text string) (store.Note, error) {
	var out store.Note
	err := s.pool.QueryRow(ctx, `
		UPDATE notes SET body = $1, updated_at = now()
		WHERE id = 1
		RETURNING body, updated_at`, text).
		Scan(&out.Text, &out.UpdatedAt)
	if err != nil {
		return store.Note{}, fmt.Errorf("update note: %w", err)
	}
	return out, nil
}

// Tasks returns open tasks oldest first, then finished ones, most recently finished first.
func (s *Store) Tasks(ctx context.Context) ([]store.Task, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, title, done_at IS NOT NULL
		FROM tasks
		ORDER BY done_at IS NOT NULL, done_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("select tasks: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[store.Task])
	if err != nil {
		return nil, fmt.Errorf("select tasks: %w", err)
	}
	return out, nil
}

// AddTask adds an open task.
func (s *Store) AddTask(ctx context.Context, title string) (store.Task, error) {
	out := store.Task{Title: title}
	if err := s.pool.QueryRow(ctx, "INSERT INTO tasks (title) VALUES ($1) RETURNING id", title).Scan(&out.ID); err != nil {
		return store.Task{}, fmt.Errorf("insert task: %w", err)
	}
	return out, nil
}

// SetTaskDone marks a task done or open again. Marking a done task done keeps
// its original time, so a repeated request doesn't reorder the list.
func (s *Store) SetTaskDone(ctx context.Context, id int64, done bool) (store.Task, error) {
	var out store.Task
	err := s.pool.QueryRow(ctx, `
		UPDATE tasks SET done_at = CASE WHEN $2 THEN COALESCE(done_at, now()) END
		WHERE id = $1
		RETURNING id, title, done_at IS NOT NULL`, id, done).
		Scan(&out.ID, &out.Title, &out.Done)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Task{}, store.ErrNotFound
	}
	if err != nil {
		return store.Task{}, fmt.Errorf("update task: %w", err)
	}
	return out, nil
}

// DeleteTask removes a task by ID.
func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM tasks WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// DeleteDoneTasks removes every finished task and reports how many there were.
func (s *Store) DeleteDoneTasks(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM tasks WHERE done_at IS NOT NULL")
	if err != nil {
		return 0, fmt.Errorf("delete done tasks: %w", err)
	}
	return tag.RowsAffected(), nil
}
