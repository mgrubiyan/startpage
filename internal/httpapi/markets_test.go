package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/markets"
)

func marketsEnv(t *testing.T, demo bool) (*testEnv, *fakeSource[markets.Board], *fakeSource[markets.Board]) {
	t.Helper()
	crypto := &fakeSource[markets.Board]{value: markets.Board{
		Source: "Binance",
		Quotes: []markets.Quote{
			{Symbol: "BTC", Name: "Bitcoin", Price: 74894, ChangePct: -1.3, Currency: "USDT", At: testNow},
			{Symbol: "ETH", Name: "Ethereum", Price: 2132.99, ChangePct: 2.5, Currency: "USDT"},
		},
	}}
	stocks := &fakeSource[markets.Board]{value: markets.Board{
		Source: "Московская биржа", Delay: 15 * time.Minute,
		Quotes: []markets.Quote{{Symbol: "IMOEX", Name: "Индекс МосБиржи", Price: 2471.46, ChangePct: 0.31}},
	}}
	env := newTestEnv(t, func(d *Deps) {
		d.Crypto, d.Stocks, d.Demo = crypto, stocks, demo
		if demo {
			d.Auth = nil
		}
	})
	return env, crypto, stocks
}

func TestMarkets(t *testing.T) {
	env, _, stocks := marketsEnv(t, false)
	stocks.stale = true

	rec := env.do(t, "GET", "/api/markets", "")
	got := decode[marketsDTO](t, rec)
	if !got.Available || got.Crypto == nil || got.Stocks == nil {
		t.Fatalf("markets = %+v", got)
	}
	c, s := got.Crypto, got.Stocks
	if c.Source != "Binance" || c.DelayMinutes != 0 || c.Stale || len(c.Quotes) != 2 || c.Error != "" {
		t.Fatalf("crypto = %+v", c)
	}
	if q := c.Quotes[0]; q.Symbol != "BTC" || q.Price != 74894 || q.Change != -1.3 || q.Currency != "USDT" || q.At == nil || !q.At.Equal(testNow) {
		t.Fatalf("btc = %+v", q)
	}
	if c.Quotes[1].At != nil {
		t.Fatal("a quote without a time must leave it out")
	}
	if s.DelayMinutes != 15 || !s.Stale || s.Quotes[0].Currency != "" || s.FetchedAt == nil {
		t.Fatalf("stocks = %+v", s)
	}
	if body := rec.Body.String(); strings.Contains(body, `"reason"`) {
		t.Fatalf("an available board needs no reason: %s", body)
	}
}

// One exchange down still shows the other.
func TestMarketsOneExchangeDown(t *testing.T) {
	env, crypto, _ := marketsEnv(t, false)
	crypto.err = errors.New("binance: GET https://data-api.binance.vision: unexpected status 451")

	rec := env.do(t, "GET", "/api/markets", "")
	got := decode[marketsDTO](t, rec)
	if rec.Code != http.StatusOK || got.Crypto.Error == "" || got.Crypto.Quotes == nil || len(got.Crypto.Quotes) != 0 || got.Stocks.Error != "" {
		t.Fatalf("markets = %d %+v", rec.Code, got)
	}
	if strings.Contains(rec.Body.String(), "451") {
		t.Fatalf("upstream detail leaked: %s", rec.Body)
	}
}

func TestMarketsAllDown(t *testing.T) {
	env, crypto, stocks := marketsEnv(t, false)
	crypto.err, stocks.err = errors.New("down"), errors.New("down")
	if rec := env.do(t, "GET", "/api/markets", ""); rec.Code != http.StatusBadGateway {
		t.Fatalf("all down = %d %s", rec.Code, rec.Body)
	}
}

// The public demo never asks the exchanges: their free data is for personal use.
func TestMarketsOffInDemo(t *testing.T) {
	env, crypto, stocks := marketsEnv(t, true)
	got := decode[marketsDTO](t, env.doAnon(t, "GET", "/api/markets", ""))
	if got.Available || got.Reason == "" || got.Crypto != nil || got.Stocks != nil {
		t.Fatalf("demo markets = %+v", got)
	}
	if len(crypto.keys)+len(stocks.keys) != 0 {
		t.Fatal("demo mode queried an exchange")
	}
}

func TestMarketsNotConfigured(t *testing.T) {
	got := decode[marketsDTO](t, newTestEnv(t).do(t, "GET", "/api/markets", ""))
	if got.Available || got.Reason == "" {
		t.Fatalf("unconfigured markets = %+v", got)
	}
}

func TestMarketsNeedsLogin(t *testing.T) {
	env, crypto, _ := marketsEnv(t, false)
	if rec := env.doAnon(t, "GET", "/api/markets", ""); rec.Code != http.StatusUnauthorized || len(crypto.keys) != 0 {
		t.Fatalf("anonymous = %d, exchange calls %d", rec.Code, len(crypto.keys))
	}
}
