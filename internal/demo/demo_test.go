package demo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mgrubiyan/startpage/internal/store"
)

type fakeStore struct {
	services []store.Service
	listErr  error
	note     string
	tasks    []store.Task
}

func (f *fakeStore) Note(context.Context) (store.Note, error) { return store.Note{Text: f.note}, nil }

func (f *fakeStore) SaveNote(_ context.Context, text string) (store.Note, error) {
	f.note = text
	return store.Note{Text: text}, nil
}

func (f *fakeStore) Tasks(context.Context) ([]store.Task, error) { return f.tasks, nil }

func (f *fakeStore) AddTask(_ context.Context, title string) (store.Task, error) {
	t := store.Task{ID: int64(len(f.tasks) + 1), Title: title}
	f.tasks = append(f.tasks, t)
	return t, nil
}

func (f *fakeStore) SetTaskDone(_ context.Context, id int64, done bool) (store.Task, error) {
	f.tasks[id-1].Done = done
	return f.tasks[id-1], nil
}

func TestSeedFillsTheDeskOnce(t *testing.T) {
	ctx := context.Background()
	f := &fakeStore{}
	for range 2 { // a restart must not duplicate the tasks
		if err := Seed(ctx, f, "http://x"); err != nil {
			t.Fatalf("Seed: %v", err)
		}
	}
	want := Tasks()
	if f.note != Note || len(f.tasks) != len(want) {
		t.Fatalf("note %q, %d tasks; want the demo note and %d tasks", f.note, len(f.tasks), len(want))
	}
	for i, task := range f.tasks {
		if task.Title != want[i].Title || task.Done != want[i].Done {
			t.Errorf("task %d = %+v, want %+v", i, task, want[i])
		}
	}
}

func TestSeedKeepsAnExistingDesk(t *testing.T) {
	for name, f := range map[string]*fakeStore{
		"note written": {note: "mine"},
		"tasks added":  {tasks: []store.Task{{ID: 1, Title: "mine"}}},
	} {
		t.Run(name, func(t *testing.T) {
			note, tasks := f.note, len(f.tasks)
			if err := Seed(context.Background(), f, "http://x"); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			if f.note != note || len(f.tasks) != tasks {
				t.Fatalf("Seed touched a used desk: note %q, %d tasks", f.note, len(f.tasks))
			}
		})
	}
}

func TestDeskFitsStoreLimits(t *testing.T) {
	if n := len([]rune(Note)); n > 10000 {
		t.Errorf("note has %d runes", n)
	}
	for _, task := range Tasks() {
		if n := len([]rune(task.Title)); n == 0 || n > 200 {
			t.Errorf("task %q has %d runes", task.Title, n)
		}
	}
}

func (f *fakeStore) Services(context.Context) ([]store.Service, error) {
	return f.services, f.listErr
}

func (f *fakeStore) AddService(_ context.Context, s store.Service) (store.Service, error) {
	s.ID = int64(len(f.services) + 1)
	f.services = append(f.services, s)
	return s, nil
}

func TestSeedFillsEmptyStoreOnce(t *testing.T) {
	ctx := context.Background()
	f := &fakeStore{}
	if err := Seed(ctx, f, "http://127.0.0.1:8080"); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	want := len(Services(""))
	if len(f.services) != want {
		t.Fatalf("seeded %d services, want %d", len(f.services), want)
	}
	if f.services[0].URL != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("self check URL = %q", f.services[0].URL)
	}

	// A restart must not duplicate the rows.
	if err := Seed(ctx, f, "http://127.0.0.1:8080"); err != nil {
		t.Fatalf("second Seed: %v", err)
	}
	if len(f.services) != want {
		t.Fatalf("second Seed added rows: %d", len(f.services))
	}
}

func TestSeedKeepsExistingServices(t *testing.T) {
	f := &fakeStore{services: []store.Service{{ID: 1, Name: "mine", URL: "https://example.com"}}}
	if err := Seed(context.Background(), f, "http://x"); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if len(f.services) != 1 {
		t.Fatalf("Seed touched a non-empty store: %+v", f.services)
	}
}

func TestSeedReportsStoreErrors(t *testing.T) {
	f := &fakeStore{listErr: errors.New("db down")}
	err := Seed(context.Background(), f, "http://x")
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v", err)
	}
}

// The services table allows names of 1 to 60 characters and URLs up to 2048.
func TestServicesFitStoreLimits(t *testing.T) {
	for _, s := range Services("http://127.0.0.1:8080") {
		if n := len([]rune(s.Name)); n == 0 || n > 60 {
			t.Errorf("name %q has %d runes", s.Name, n)
		}
		if len(s.URL) > 2048 || (!strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://")) {
			t.Errorf("url %q is not a valid http(s) URL", s.URL)
		}
	}
}
