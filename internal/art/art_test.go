package art

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

var jpeg = []byte("\xff\xd8\xff\xe0 fake jpeg")

// museum fakes the API and the CDN on one TLS server, so the https and host checks run for real.
type museum struct {
	srv     *httptest.Server
	records []map[string]any
	image   func(w http.ResponseWriter, r *http.Request)
	images  atomic.Int32

	mu    sync.Mutex
	depts map[string]int // department filter of each API request
}

func newMuseum(t *testing.T) *museum {
	t.Helper()
	m := &museum{
		image: func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpeg) },
		depts: map[string]int{},
	}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img/") {
			m.images.Add(1)
			m.image(w, r)
			return
		}
		m.mu.Lock()
		m.depts[r.URL.Query().Get("department")]++
		m.mu.Unlock()
		data := m.records
		if r.URL.Query().Get("limit") == "1" {
			data = data[:1]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"info": map[string]any{"total": len(m.records)}, "data": data})
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *museum) client() *Client {
	u, _ := url.Parse(m.srv.URL)
	return newClient(m.srv.Client(), m.srv.URL+"/api/artworks/", u.Host)
}

func (m *museum) painting(id int, width, height, size any, imgURL string) map[string]any {
	if imgURL == "" {
		imgURL = fmt.Sprintf("%s/img/%d_print.jpg", m.srv.URL, id)
	}
	return map[string]any{
		"id":            id,
		"title":         fmt.Sprintf("Painting %d", id),
		"creation_date": "1890",
		"url":           fmt.Sprintf("https://clevelandart.org/art/%d", id),
		"creators":      []map[string]any{{"description": "Claude Monet (French, 1840–1926)"}},
		"images": map[string]any{"print": map[string]any{
			"url": imgURL, "width": width, "height": height, "filesize": size,
		}},
	}
}

func TestPickSkipsUnsuitablePaintings(t *testing.T) {
	m := newMuseum(t)
	unsuitable := map[string]map[string]any{
		"portrait":              m.painting(1, "2000", "3000", "1000", ""),
		"too small":             m.painting(2, "2000", "1000", "1000", ""),
		"sizes missing":         m.painting(3, "", "", "", ""),
		"file too large":        m.painting(4, "3400", "2000", "9000000", ""),
		"not https":             m.painting(5, "3400", "2000", "1000", "http://cdn"),
		"another host":          m.painting(6, "3400", "2000", "1000", "https://x.y"),
		"nearly square":         m.painting(7, "3400", "3000", "1000", ""),
		"wider than 2:1 (leaf)": m.painting(10, "3400", "508", "1000", ""),
		"no image":              {"id": 9, "images": nil},
	}
	for name, rec := range unsuitable {
		m.records = []map[string]any{rec}
		if a, err := m.client().Pick(context.Background(), "2026-11-08"); err == nil {
			t.Errorf("%s: picked %d", name, a.ID)
		}
	}
	if n := m.images.Load(); n != 0 {
		t.Fatalf("downloaded %d unsuitable images", n)
	}

	m.records = nil
	for _, rec := range unsuitable {
		m.records = append(m.records, rec)
	}
	m.records = append(m.records, m.painting(8, "3400", "2000", "2500000", ""))
	got, err := m.client().Pick(context.Background(), "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 8 || got.Artist != "Claude Monet" || got.Title != "Painting 8" || got.Date != "1890" ||
		got.URL != "https://clevelandart.org/art/8" || !bytes.Equal(got.Image, jpeg) {
		t.Fatalf("Pick = %+v", got)
	}
	if n := m.images.Load(); n != 1 {
		t.Fatalf("downloaded %d images, want 1", n)
	}
}

func TestPickIsStableForADay(t *testing.T) {
	m := newMuseum(t)
	for id := range 20 {
		m.records = append(m.records, m.painting(id+1, "3400", "2000", "1000", ""))
	}
	c := m.client()
	seen := map[int]bool{}
	for _, day := range []string{"2026-11-09", "2026-11-10", "2026-11-11", "2026-11-12"} {
		a, err := c.Pick(context.Background(), day)
		if err != nil {
			t.Fatal(err)
		}
		seen[a.ID] = true
	}
	if len(seen) < 2 {
		t.Fatal("four different days all gave the same painting")
	}
	// Every request is narrowed to an easel-painting department, and days rotate between them.
	for d := range m.depts {
		if !slices.Contains(departments, d) {
			t.Fatalf("API queried with department %q", d)
		}
	}
	if len(m.depts) < 2 {
		t.Fatalf("four days used departments %v", m.depts)
	}
	first, _ := c.Pick(context.Background(), "2026-11-08")
	again, _ := c.Pick(context.Background(), "2026-11-08")
	if first.ID == 0 || first.ID != again.ID {
		t.Fatalf("same day gave %d and %d", first.ID, again.ID)
	}
}

func TestPickRefusesBadImages(t *testing.T) {
	tests := map[string]func(w http.ResponseWriter, r *http.Request){
		"not found": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"not a jpeg": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html><script>alert(1)</script></html>"))
		},
		"over the cap": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(append(append([]byte{}, jpeg...), make([]byte, maxImageBytes)...))
		},
	}
	for name, handler := range tests {
		t.Run(name, func(t *testing.T) {
			m := newMuseum(t)
			m.records = []map[string]any{m.painting(1, "3400", "2000", "1000", "")}
			m.image = handler
			if a, err := m.client().Pick(context.Background(), "2026-11-08"); err == nil {
				t.Fatalf("Pick accepted the image: %d bytes", len(a.Image))
			}
		})
	}
}

// A CDN redirect must not walk the server into fetching an arbitrary host.
func TestPickRefusesRedirectOffTheMuseum(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(jpeg)
	}))
	defer other.Close()

	m := newMuseum(t)
	m.records = []map[string]any{m.painting(1, "3400", "2000", "1000", "")}
	m.image = func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/internal", http.StatusFound)
	}
	if _, err := m.client().Pick(context.Background(), "2026-11-08"); err == nil {
		t.Fatal("Pick followed a redirect to another host")
	}
	if hits.Load() != 0 {
		t.Fatal("the other host was contacted")
	}
}

func TestArtworkKeepsOnlyMuseumURLs(t *testing.T) {
	for raw, want := range map[string]string{
		"https://clevelandart.org/art/1":     "https://clevelandart.org/art/1",
		"https://www.clevelandart.org/art/1": "https://www.clevelandart.org/art/1",
		"javascript:alert(1)":                "",
		"http://clevelandart.org/art/1":      "",
		"https://clevelandart.org.evil/x":    "",
		"https://user@clevelandart.org/x":    "",
	} {
		if got := (record{URL: raw}).artwork(nil).URL; got != want {
			t.Errorf("URL %q -> %q, want %q", raw, got, want)
		}
	}
}

func TestFlexInt(t *testing.T) {
	var v struct{ A, B, C, D, E flexInt }
	if err := json.Unmarshal([]byte(`{"A":"3400","B":"","C":"n/a","D":12,"E":null}`), &v); err != nil {
		t.Fatal(err)
	}
	if v.A != 3400 || v.B != 0 || v.C != 0 || v.D != 12 || v.E != 0 {
		t.Fatalf("got %+v", v)
	}
}
