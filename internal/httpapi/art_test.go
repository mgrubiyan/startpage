package httpapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mgrubiyan/startpage/internal/art"
)

var testArtwork = art.Artwork{
	ID:     42,
	Title:  "Water Lilies",
	Artist: "Claude Monet",
	Date:   "1915–26",
	URL:    "https://clevelandart.org/art/1960.81",
	Image:  []byte("\xff\xd8\xff\xe0 jpeg"),
}

type fakeArt struct {
	artwork art.Artwork
	err     error
}

func (f *fakeArt) Today(context.Context) (art.Artwork, error) { return f.artwork, f.err }

func TestArtDescribesTodaysPainting(t *testing.T) {
	rec := newTestEnv(t).do(t, "GET", "/api/art", "")
	got := decode[artDTO](t, rec)
	want := artDTO{Title: "Water Lilies", Artist: "Claude Monet", Date: "1915–26",
		URL: "https://clevelandart.org/art/1960.81", Image: "/api/art/image?v=42"}
	if got != want {
		t.Fatalf("GET /api/art = %+v, want %+v", got, want)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("metadata Cache-Control = %q", cc)
	}
}

func TestArtImageCaching(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		path, cacheControl string
	}{
		{"/api/art/image?v=42", "private, max-age=86400, immutable"},
		{"/api/art/image", "no-store"},
	}
	for _, tt := range tests {
		rec := env.do(t, "GET", tt.path, "")
		if rec.Code != http.StatusOK || rec.Body.String() != string(testArtwork.Image) {
			t.Fatalf("GET %s = %d, %d bytes", tt.path, rec.Code, rec.Body.Len())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
			t.Errorf("GET %s: Content-Type = %q", tt.path, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != tt.cacheControl {
			t.Errorf("GET %s: Cache-Control = %q, want %q", tt.path, cc, tt.cacheControl)
		}
	}
}

// A URL from yesterday's /api/art must not get today's image: it would show
// under yesterday's caption, and be cached under the wrong v.
func TestArtImageForAnotherPaintingIsNotFound(t *testing.T) {
	rec := newTestEnv(t).do(t, "GET", "/api/art/image?v=41", "")
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") == "image/jpeg" {
		t.Fatalf("GET stale v = %d %q, want 404 JSON", rec.Code, rec.Header().Get("Content-Type"))
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

func TestArtErrors(t *testing.T) {
	env := newTestEnv(t)
	for _, tt := range []struct {
		err  error
		want int
	}{
		{art.ErrUnavailable, http.StatusServiceUnavailable},
		{errors.New("museum down"), http.StatusBadGateway},
	} {
		env.art.err = tt.err
		for _, path := range []string{"/api/art", "/api/art/image?v=42"} {
			rec := env.do(t, "GET", path, "")
			if rec.Code != tt.want || rec.Header().Get("Content-Type") == "image/jpeg" {
				t.Errorf("%v: GET %s = %d %q, want %d JSON", tt.err, path, rec.Code, rec.Header().Get("Content-Type"), tt.want)
			}
		}
	}
}
