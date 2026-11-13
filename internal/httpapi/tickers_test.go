package httpapi

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsTickers(t *testing.T) {
	env := newTestEnv(t)
	got := decode[settingsDTO](t, env.do(t, "PUT", "/api/settings",
		`{"searchEngine":"g","city":"Москва","crypto":[" ton","btc","TON",""],"shares":["sberp","t"]}`))
	if !reflect.DeepEqual(got.Crypto, []string{"TON", "BTC"}) || !reflect.DeepEqual(got.Shares, []string{"SBERP", "T"}) {
		t.Fatalf("cleaned = %+v", got)
	}
	// A page from before the lists existed sends neither: both are kept.
	got = decode[settingsDTO](t, env.do(t, "PUT", "/api/settings", `{"searchEngine":"y","city":"Москва"}`))
	if got.SearchEngine != "y" || !reflect.DeepEqual(got.Crypto, []string{"TON", "BTC"}) || !reflect.DeepEqual(got.Shares, []string{"SBERP", "T"}) {
		t.Fatalf("lists left out = %+v", got)
	}
	// An empty list is a choice, and is [] in JSON, not null.
	rec := env.do(t, "PUT", "/api/settings", `{"searchEngine":"y","city":"Москва","crypto":[]}`)
	if !strings.Contains(rec.Body.String(), `"crypto":[]`) || !strings.Contains(rec.Body.String(), `"shares":["SBERP","T"]`) {
		t.Fatalf("emptied = %s", rec.Body)
	}
}

func TestSettingsTickersRejected(t *testing.T) {
	env := newTestEnv(t)
	for _, tt := range []struct{ name, body, want string }{
		{"not a ticker", `{"searchEngine":"g","city":"Москва","crypto":["BTC-USD"]}`, "crypto:"},
		{"cyrillic", `{"searchEngine":"g","city":"Москва","shares":["СБЕР"]}`, "shares:"},
		{"one-letter coin", `{"searchEngine":"g","city":"Москва","crypto":["B"]}`, "crypto:"},
		{"too many", `{"searchEngine":"g","city":"Москва","shares":["A","B","C","D","E","F","G","H","I"]}`, "at most 8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.do(t, "PUT", "/api/settings", tt.body)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("got %d %s, want 400 with %q", rec.Code, rec.Body, tt.want)
			}
		})
	}
	if s := env.store.settings; !reflect.DeepEqual(s.Crypto, []string{"BTC", "ETH"}) {
		t.Fatalf("a rejected list was saved: %+v", s)
	}
}

// The boards are asked for the settings' tickers: they are the cache key.
func TestMarketsUseSettings(t *testing.T) {
	env, crypto, stocks := marketsEnv(t, false)
	crypto.value.Missing = []string{"BTCC"}
	env.store.settings.Crypto = []string{"BTC", "BTCC"}
	env.store.settings.Shares = []string{"SBER", "YDEX"}

	got := decode[marketsDTO](t, env.do(t, "GET", "/api/markets", ""))
	if len(crypto.keys) != 1 || crypto.keys[0] != "BTC,BTCC" || len(stocks.keys) != 1 || stocks.keys[0] != "SBER,YDEX" {
		t.Fatalf("keys: crypto %v, stocks %v", crypto.keys, stocks.keys)
	}
	if got.Crypto == nil || !reflect.DeepEqual(got.Crypto.Missing, []string{"BTCC"}) {
		t.Fatalf("crypto = %+v", got.Crypto)
	}
}

// No coins listed: no crypto board at all, while the shares board keeps the index.
func TestMarketsWithoutCoins(t *testing.T) {
	env, crypto, stocks := marketsEnv(t, false)
	env.store.settings.Crypto = []string{}
	env.store.settings.Shares = nil

	got := decode[marketsDTO](t, env.do(t, "GET", "/api/markets", ""))
	if !got.Available || got.Crypto != nil || got.Stocks == nil || len(crypto.keys) != 0 {
		t.Fatalf("markets = %+v, crypto asked %d times", got, len(crypto.keys))
	}
	if len(stocks.keys) != 1 || stocks.keys[0] != "" {
		t.Fatalf("stocks keys = %q, want the index alone", stocks.keys)
	}
}
