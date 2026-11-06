package suggest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func newTestClient(t *testing.T, primary, fallback http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var fallbackCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/primary", primary)
	mux.HandleFunc("/fallback", func(w http.ResponseWriter, r *http.Request) {
		fallbackCalls.Add(1)
		if fallback == nil {
			t.Error("fallback must not be called")
			return
		}
		fallback(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &Client{http: srv.Client(), providers: []provider{
		{"primary", func(q string) string { return srv.URL + "/primary?q=" + url.QueryEscape(q) }},
		{"fallback", func(q string) string { return srv.URL + "/fallback?q=" + url.QueryEscape(q) }},
	}}
	return c, &fallbackCalls
}

func respond(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func TestPrimaryWins(t *testing.T) {
	c, fallbackCalls := newTestClient(t,
		respond(`["погода",["погода","погода спб","погода завтра"],[],{"google:suggesttype":["QUERY"]}]`),
		respond(`["x",["from fallback"]]`))
	got, err := c.Suggest(context.Background(), "погода")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "погода|погода спб|погода завтра" {
		t.Fatalf("got %q", got)
	}
	if fallbackCalls.Load() != 0 {
		t.Fatal("fallback called although primary succeeded")
	}
}

func TestFallbackOnPrimaryFailure(t *testing.T) {
	for name, primary := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTooManyRequests) },
		"shape":  respond(`{"error":"nope"}`),
		"list":   respond(`["q", "not a list"]`),
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, primary, respond(`["q",["a","b"]]`))
			got, err := c.Suggest(context.Background(), "q")
			if err != nil || strings.Join(got, ",") != "a,b" {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestAllProvidersFail(t *testing.T) {
	fail := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }
	c, _ := newTestClient(t, fail, fail)
	_, err := c.Suggest(context.Background(), "q")
	if err == nil || !strings.Contains(err.Error(), "primary") || !strings.Contains(err.Error(), "fallback") {
		t.Fatalf("error should name both providers: %v", err)
	}
}

func TestResultsCappedAndNeverNil(t *testing.T) {
	c, _ := newTestClient(t, respond(`["q",["1","2","3","4","5","6","7","8","9","10"]]`), nil)
	got, err := c.Suggest(context.Background(), "q")
	if err != nil || len(got) != MaxResults {
		t.Fatalf("len = %d, %v", len(got), err)
	}

	c, _ = newTestClient(t, respond(`["q",[]]`), nil)
	got, err = c.Suggest(context.Background(), "q")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty result = %#v, %v", got, err)
	}
}
