package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

const token = "123456:SECRET-token_value"

func newBot(t *testing.T, h http.HandlerFunc) *Bot {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	b := New(srv.Client(), token, "-100500")
	b.base = srv.URL
	return b
}

func TestSendPostsPlainMessage(t *testing.T) {
	var got map[string]any
	b := newBot(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bot"+token+"/sendMessage" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	})

	if err := b.Send(context.Background(), "🔴 <b>bot</b> is down"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"chat_id": "-100500", "text": "🔴 <b>bot</b> is down", "disable_web_page_preview": true}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["parse_mode"]; ok {
		t.Error("parse_mode is set: names with < or * would break the message")
	}
}

func TestSendTruncatesLongText(t *testing.T) {
	var text string
	b := newBot(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		text = body.Text
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	if err := b.Send(context.Background(), strings.Repeat("я", 5000)); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(text); n != maxRunes+1 || !strings.HasSuffix(text, "…") {
		t.Errorf("sent %d runes, want %d ending in …", n, maxRunes+1)
	}
}

func TestSendReportsAPIErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"error status": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
		},
		"error status with ok body": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError) // e.g. a proxy in between
			_, _ = w.Write([]byte(`{"ok":true}`))
		},
		"ok false with 200": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"ok":false,"description":"Forbidden: bot was blocked by the user"}`))
		},
		"not json": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>bad gateway</html>`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := newBot(t, h).Send(context.Background(), "hi")
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("error leaks the token: %v", err)
			}
		})
	}
}

func TestSendErrorNeverLeaksToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	b := New(srv.Client(), token, "1")
	b.base = srv.URL
	srv.Close() // connection refused: the client returns a *url.Error with the full URL

	err := b.Send(context.Background(), "hi")
	if err == nil {
		t.Fatal("no error from a closed server")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error leaks the token: %v", err)
	}
}
