package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mgrubiyan/startpage/internal/store"
)

const (
	// maxNoteRunes matches the CHECK in migration 005. 10 000 four-byte runes
	// plus JSON quoting still fit the 64 KiB body limit.
	maxNoteRunes = 10000
	maxTaskRunes = 200
)

type noteDTO struct {
	Text      string    `json:"text"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type noteInput struct {
	Text string `json:"text"`
}

type taskDTO struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

type taskInput struct {
	Title string `json:"title"`
}

type taskPatch struct {
	Done *bool `json:"done"` // required: a missing field must not silently reopen a task
}

// cleanNote validates the scratchpad. Unlike cleanText it keeps the text as typed:
// empty is fine, nothing is trimmed (the textarea would jump), and line breaks
// and tabs are allowed. CRLF and lone CR become LF.
func cleanNote(s string) (string, error) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	switch {
	case !utf8.ValidString(s):
		return "", errors.New("must be valid UTF-8")
	case utf8.RuneCountInString(s) > maxNoteRunes:
		return "", fmt.Errorf("must be at most %d characters", maxNoteRunes)
	case strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0:
		return "", errors.New("must not contain control characters other than line breaks and tabs")
	}
	return s, nil
}

// ---------- note ----------

func (a *api) getNote(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.Note(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noteDTO(n))
}

func (a *api) putNote(w http.ResponseWriter, r *http.Request) {
	var in noteInput
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	text, err := cleanNote(in.Text)
	if err != nil {
		writeError(w, http.StatusBadRequest, "text: "+err.Error())
		return
	}
	saved, err := a.store.SaveNote(r.Context(), text)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, noteDTO(saved))
}

// ---------- tasks ----------

func (a *api) listTasks(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Tasks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	out := make([]taskDTO, 0, len(list))
	for _, t := range list {
		out = append(out, taskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) createTask(w http.ResponseWriter, r *http.Request) {
	var in taskInput
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	title, err := cleanText(in.Title, maxTaskRunes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "title: "+err.Error())
		return
	}
	t, err := a.store.AddTask(r.Context(), title)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, taskDTO(t))
}

func (a *api) patchTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	var in taskPatch
	if err := decodeJSON(w, r, &in); err != nil {
		badBody(w, err)
		return
	}
	if in.Done == nil {
		writeError(w, http.StatusBadRequest, "done is required")
		return
	}
	t, err := a.store.SetTaskDone(r.Context(), id, *in.Done)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case err != nil:
		a.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, taskDTO(t))
	}
}

func (a *api) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	a.deleted(w, r, a.store.DeleteTask(r.Context(), id))
}

type deletedDTO struct {
	Deleted int64 `json:"deleted"`
}

func (a *api) deleteDoneTasks(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.DeleteDoneTasks(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deletedDTO{Deleted: n})
}
