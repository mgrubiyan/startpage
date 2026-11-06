package rates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, body string, status int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=UTF-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.Client())
	c.url = srv.URL
	return c
}

func TestLatest(t *testing.T) {
	c := newTestClient(t, `{
		"Date": "2026-11-07T11:30:00+03:00",
		"Valute": {
			"USD": {"Nominal": 1, "Value": 82.1404, "Previous": 81.9},
			"EUR": {"Nominal": 1, "Value": 92.19, "Previous": 92.5},
			"HUF": {"Nominal": 100, "Value": 24.5, "Previous": 24.4}
		}}`, 200)
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.USD != (Rate{82.1404, 81.9}) || got.EUR != (Rate{92.19, 92.5}) {
		t.Fatalf("rates = %+v", got)
	}
	if want := time.Date(2026, 11, 7, 8, 30, 0, 0, time.UTC); !got.Date.Equal(want) {
		t.Fatalf("date = %v", got.Date)
	}
}

func TestLatestNormalizesNominal(t *testing.T) {
	c := newTestClient(t, `{"Valute":{"USD":{"Nominal":10,"Value":821,"Previous":819},"EUR":{"Nominal":1,"Value":92,"Previous":92}}}`, 200)
	got, err := c.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.USD.Value != 82.1 || got.USD.Previous != 81.9 {
		t.Fatalf("USD = %+v, want per-unit rate", got.USD)
	}
}

func TestLatestErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		want   string
	}{
		{"http error", "", 503, "unexpected status 503"},
		{"missing EUR", `{"Valute":{"USD":{"Nominal":1,"Value":80,"Previous":80}}}`, 200, "EUR missing"},
		{"not json", "var rates = {};", 200, "decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newTestClient(t, tt.body, tt.status).Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
