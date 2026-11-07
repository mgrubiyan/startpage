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
