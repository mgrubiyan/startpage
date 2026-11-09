package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mgrubiyan/startpage/internal/store"
)

func noteBody(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(noteInput{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNoteRoundTrip(t *testing.T) {
	env := newTestEnv(t)
	rec := env.do(t, "PUT", "/api/note", noteBody(t, "  купить молоко\r\n\tпозвонить\r"))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	// Kept as typed: no trimming, tabs stay, CRLF and CR become LF.
	const want = "  купить молоко\n\tпозвонить\n"
	if got := decode[noteDTO](t, rec); got.Text != want || !got.UpdatedAt.Equal(testNow) {
		t.Fatalf("PUT echo = %+v, want %q", got, want)
	}
	if got := decode[noteDTO](t, env.do(t, "GET", "/api/note", "")); got.Text != want {
		t.Fatalf("GET = %q, want %q", got.Text, want)
	}
	// Clearing the note is a normal edit, not an error.
	if rec := env.do(t, "PUT", "/api/note", noteBody(t, "")); rec.Code != http.StatusOK || env.store.note.Text != "" {
		t.Fatalf("empty PUT = %d, stored %q", rec.Code, env.store.note.Text)
	}
}

func TestNoteValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		text string
		code int
	}{
		"at the limit":                   {strings.Repeat("я", maxNoteRunes), http.StatusOK},
		"over the limit":                 {strings.Repeat("я", maxNoteRunes+1), http.StatusBadRequest},
		"control character":              {"a\x00b", http.StatusBadRequest},
		"escape sequence":                {"a\x1b[31mb", http.StatusBadRequest},
		"limit in emoji fits the body":   {strings.Repeat("🙂", maxNoteRunes), http.StatusOK},
		"limit in escapes fits the body": {strings.Repeat("\n", maxNoteRunes), http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t)
			if rec := env.do(t, "PUT", "/api/note", noteBody(t, tc.text)); rec.Code != tc.code {
				t.Fatalf("PUT = %d %s, want %d", rec.Code, rec.Body, tc.code)
			}
		})
	}
}

func TestTasksFlow(t *testing.T) {
	env := newTestEnv(t)

	rec := env.do(t, "POST", "/api/tasks", `{"title":"  починить бота  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	task := decode[taskDTO](t, rec)
	if task.Title != "починить бота" || task.Done {
		t.Fatalf("created %+v", task)
	}
	env.do(t, "POST", "/api/tasks", `{"title":"second"}`)

	rec = env.do(t, "PATCH", "/api/tasks/"+itoa(task.ID), `{"done":true}`)
	if rec.Code != http.StatusOK || !decode[taskDTO](t, rec).Done {
		t.Fatalf("PATCH done = %d %s", rec.Code, rec.Body)
	}
	if list := decode[[]taskDTO](t, env.do(t, "GET", "/api/tasks", "")); len(list) != 2 {
		t.Fatalf("GET = %+v", list)
	}

	// "completed" must reach the bulk handler, not deleteTask's id check.
	rec = env.do(t, "DELETE", "/api/tasks/completed", "")
	if rec.Code != http.StatusOK || decode[deletedDTO](t, rec).Deleted != 1 {
		t.Fatalf("DELETE completed = %d %s", rec.Code, rec.Body)
	}
	if len(env.store.tasks) != 1 || env.store.tasks[0].Title != "second" {
		t.Fatalf("left %+v", env.store.tasks)
	}

	if rec := env.do(t, "DELETE", "/api/tasks/"+itoa(env.store.tasks[0].ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
}

func TestTaskErrors(t *testing.T) {
	env := newTestEnv(t)
	env.store.tasks = []store.Task{{ID: 7, Title: "x", Done: true}}
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{
		{"POST", "/api/tasks", `{"title":"   "}`, http.StatusBadRequest},
		{"POST", "/api/tasks", `{"title":"` + strings.Repeat("я", maxTaskRunes+1) + `"}`, http.StatusBadRequest},
		{"POST", "/api/tasks", `{"title":"a\nb"}`, http.StatusBadRequest},
		{"PATCH", "/api/tasks/7", `{}`, http.StatusBadRequest}, // a missing done must not reopen it
		{"PATCH", "/api/tasks/7", `{"done":"yes"}`, http.StatusBadRequest},
		{"PATCH", "/api/tasks/0", `{"done":true}`, http.StatusBadRequest},
		{"PATCH", "/api/tasks/99", `{"done":true}`, http.StatusNotFound},
		{"DELETE", "/api/tasks/99", ``, http.StatusNotFound},
		{"DELETE", "/api/tasks/abc", ``, http.StatusBadRequest},
	} {
		if rec := env.do(t, tc.method, tc.path, tc.body); rec.Code != tc.code {
			t.Errorf("%s %s %s = %d %s, want %d", tc.method, tc.path, tc.body, rec.Code, rec.Body, tc.code)
		}
	}
	if !env.store.tasks[0].Done {
		t.Fatal("a rejected PATCH changed the task")
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
