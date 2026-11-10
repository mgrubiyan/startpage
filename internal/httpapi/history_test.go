package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/store"
)

func TestServicesHistory(t *testing.T) {
	env := newTestEnv(t)
	env.store.statuses = []store.ServiceStatus{
		{Service: store.Service{ID: 1, Name: "bot", URL: "https://bot.example"}},
		{Service: store.Service{ID: 2, Name: "new", URL: "https://new.example"}},
	}
	// testNow is 12:00 UTC, so the day runs from the 13:00 slot yesterday to the 12:00 one.
	first := testNow.Add(-23 * time.Hour)
	env.store.history = map[int64][]store.CheckBucket{
		1: {
			{Start: first.Add(-time.Hour), Total: 60, OK: 60},                     // a day ago: not shown
			{Start: first, Total: 60, OK: 59, AvgLatency: 120 * time.Millisecond}, // first slot
			{Start: testNow, Total: 3, OK: 0},                                     // the current hour
			{Start: testNow.Add(30 * time.Minute), Total: 1, OK: 1},               // not on the hour
		},
	}

	got := decode[[]serviceDTO](t, env.do(t, "GET", "/api/services", ""))
	if !env.store.historySince.Equal(first) || env.store.historySlot != time.Hour {
		t.Fatalf("history asked since %v in %v slots", env.store.historySince, env.store.historySlot)
	}
	if len(got) != 2 {
		t.Fatalf("services = %+v", got)
	}
	h := got[0].History
	if len(h) != HistorySlots || !h[0].Start.Equal(first) || !h[HistorySlots-1].Start.Equal(testNow) {
		t.Fatalf("slots = %d from %v to %v", len(h), h[0].Start, h[len(h)-1].Start)
	}
	if h[0] != (slotDTO{Start: first, Total: 60, OK: 59, LatencyMs: 120}) {
		t.Fatalf("first slot = %+v", h[0])
	}
	if last := h[HistorySlots-1]; last.Total != 3 || last.OK != 0 {
		t.Fatalf("current hour = %+v", last)
	}
	for i := 1; i < HistorySlots-1; i++ {
		if h[i].Total != 0 {
			t.Fatalf("slot %d = %+v, want empty", i, h[i])
		}
	}
	// A service without checks still gets its row of empty slots.
	if len(got[1].History) != HistorySlots || got[1].History[5].Total != 0 {
		t.Fatalf("new service history = %+v", got[1].History)
	}
}

// The bars are extra: if they can't be loaded, the list itself still is.
func TestServicesWithoutHistory(t *testing.T) {
	env := newTestEnv(t)
	env.store.statuses = []store.ServiceStatus{{Service: store.Service{ID: 1, Name: "bot", URL: "https://bot.example"}}}
	env.store.historyErr = errors.New("statement timeout")

	rec := env.do(t, "GET", "/api/services", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"history":[]`) {
		t.Fatalf("services = %d %s", rec.Code, rec.Body)
	}
}

func TestHistorySlotsAcrossZones(t *testing.T) {
	// The server's zone doesn't move the slots: they are whole UTC hours.
	now := time.Date(2026, 11, 10, 3, 40, 0, 0, time.FixedZone("", 5*60*60+30*60))
	slots := historySlots(nil, now)
	if want := time.Date(2026, 11, 9, 22, 0, 0, 0, time.UTC); !slots[HistorySlots-1].Start.Equal(want) {
		t.Fatalf("current slot starts %v, want %v", slots[HistorySlots-1].Start, want)
	}
}
