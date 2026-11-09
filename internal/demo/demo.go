// Package demo prepares the data a public, read-only showcase instance starts with.
package demo

import (
	"context"
	"fmt"

	"github.com/mgrubiyan/startpage/internal/store"
)

// Store is the part of the persistence layer seeding needs; *postgres.Store implements it.
type Store interface {
	Services(ctx context.Context) ([]store.Service, error)
	AddService(ctx context.Context, s store.Service) (store.Service, error)
	Note(ctx context.Context) (store.Note, error)
	SaveNote(ctx context.Context, text string) (store.Note, error)
	Tasks(ctx context.Context) ([]store.Task, error)
	AddTask(ctx context.Context, title string) (store.Task, error)
	SetTaskDone(ctx context.Context, id int64, done bool) (store.Task, error)
}

// Note is the demo's scratchpad.
const Note = `Идеи для стартовой страницы:
— тёмная тема по расписанию
— график аптайма за неделю

Заметка сохраняется сама, пока печатаешь. В демо она только для чтения.`

// Tasks are the demo's to-do list, some of them already done.
func Tasks() []store.Task {
	return []store.Task{
		{Title: "Продлить домен"},
		{Title: "Настроить бэкапы на другой сервер"},
		{Title: "Подключить уведомления в Telegram", Done: true},
		{Title: "Задеплоить на VPS", Done: true},
	}
}

// Services are the monitor targets of a fresh demo. They mix this instance's own
// health check, the upstream APIs the page itself depends on, two public
// endpoints, and one address that is always down, so the red state shows too.
// selfURL is the base URL the server is reachable on from inside its container.
func Services(selfURL string) []store.Service {
	return []store.Service{
		{Name: "Startpage API", URL: selfURL + "/healthz"},
		{Name: "Open-Meteo (погода)", URL: "https://api.open-meteo.com/v1/forecast?latitude=59.94&longitude=30.31&current=temperature_2m"},
		{Name: "Курсы ЦБ РФ", URL: "https://www.cbr-xml-daily.ru/daily_json.js"},
		{Name: "Go module proxy", URL: "https://proxy.golang.org/"},
		{Name: "GitHub Status", URL: "https://www.githubstatus.com/api/v2/status.json"},
		// Port 9 (discard) has no listener, so the probe fails with "connection refused".
		{Name: "Старый бот (выключен)", URL: "http://127.0.0.1:9/health"},
	}
}

// Seed adds the demo services when none exist yet, and the demo note and tasks
// while the note is empty and there are no tasks, so a restart duplicates nothing.
// It is meant for one instance per database: two starting at the same moment on
// an empty database could both seed. Bookmarks need no seeding: the first
// migration adds them.
func Seed(ctx context.Context, s Store, selfURL string) error {
	if err := seedServices(ctx, s, selfURL); err != nil {
		return err
	}
	return seedDesk(ctx, s)
}

// seedDesk fills the note and the to-do list while both are empty, so a demo
// database created before they existed gets them too.
func seedDesk(ctx context.Context, s Store) error {
	note, err := s.Note(ctx)
	if err != nil {
		return fmt.Errorf("demo seed: read note: %w", err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil {
		return fmt.Errorf("demo seed: list tasks: %w", err)
	}
	if note.Text != "" || len(tasks) > 0 {
		return nil
	}
	if _, err := s.SaveNote(ctx, Note); err != nil {
		return fmt.Errorf("demo seed: save note: %w", err)
	}
	for _, t := range Tasks() {
		added, err := s.AddTask(ctx, t.Title)
		if err != nil {
			return fmt.Errorf("demo seed: add task %q: %w", t.Title, err)
		}
		if t.Done {
			if _, err := s.SetTaskDone(ctx, added.ID, true); err != nil {
				return fmt.Errorf("demo seed: finish task %q: %w", t.Title, err)
			}
		}
	}
	return nil
}

func seedServices(ctx context.Context, s Store, selfURL string) error {
	existing, err := s.Services(ctx)
	if err != nil {
		return fmt.Errorf("demo seed: list services: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for _, svc := range Services(selfURL) {
		if _, err := s.AddService(ctx, svc); err != nil {
			return fmt.Errorf("demo seed: add %q: %w", svc.Name, err)
		}
	}
	return nil
}
