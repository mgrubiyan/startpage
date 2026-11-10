package markets

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

var testCoins = []Coin{{"BTC", "Bitcoin"}, {"ETH", "Ethereum"}, {"TON", "Toncoin"}}

func TestBinance(t *testing.T) {
	var gotSymbols, gotType, gotUA string
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		gotSymbols, gotType, gotUA = r.URL.Query().Get("symbols"), r.URL.Query().Get("type"), r.UserAgent()
		// ETH is answered before BTC and TON is missing: order follows the coin list.
		_, _ = w.Write([]byte(`[
			{"symbol":"ETHUSDT","openPrice":"2000.00","lastPrice":"2100.00","closeTime":1794253159007},
			{"symbol":"BTCUSDT","openPrice":"80000.00000000","lastPrice":"76000.00000000","closeTime":1794253159001}]`))
	})
	b := NewBinance(srv.Client(), testCoins)
	b.url = srv.URL

	got, err := b.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotSymbols != `["BTCUSDT","ETHUSDT","TONUSDT"]` || gotType != "MINI" || gotUA != fetch.UserAgent {
		t.Fatalf("query symbols=%s type=%s ua=%q", gotSymbols, gotType, gotUA)
	}
	if got.Source != "Binance" || got.Delay != 0 || len(got.Quotes) != 2 {
		t.Fatalf("board = %+v", got)
	}
	btc, eth := got.Quotes[0], got.Quotes[1]
	if btc.Symbol != "BTC" || btc.Name != "Bitcoin" || btc.Price != 76000 || !near(btc.ChangePct, -5) || btc.Currency != "USDT" {
		t.Fatalf("btc = %+v", btc)
	}
	if eth.Symbol != "ETH" || !near(eth.ChangePct, 5) || !eth.At.Equal(time.UnixMilli(1794253159007)) {
		t.Fatalf("eth = %+v", eth)
	}
}

func TestBinanceErrors(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		code       int
		want       string
	}{
		// One unknown symbol fails the whole request on Binance.
		{"invalid symbol", `{"code":-1121,"msg":"Invalid symbol."}`, 400, "unexpected status 400"},
		{"geo-blocked", `{"code":0,"msg":"restricted location"}`, 451, "unexpected status 451"},
		{"price not a number", `[{"symbol":"BTCUSDT","openPrice":"1","lastPrice":"n/a"}]`, 200, `lastPrice "n/a"`},
		{"zero price", `[{"symbol":"BTCUSDT","openPrice":"0","lastPrice":"5"}]`, 200, `openPrice "0"`},
		{"nothing quoted", `[]`, 200, errNoQuotes.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := serve(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			})
			b := NewBinance(srv.Client(), testCoins)
			b.url = srv.URL
			if _, err := b.Board(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func bybitServer(t *testing.T, bySymbol map[string]string) *Bybit {
	t.Helper()
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") != "spot" {
			t.Errorf("category = %q", r.URL.Query().Get("category"))
		}
		body, ok := bySymbol[r.URL.Query().Get("symbol")]
		if !ok {
			body = `{"retCode":10001,"retMsg":"Not supported symbols","result":{}}`
		}
		if body == "500" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	b := NewBybit(srv.Client(), testCoins)
	b.url = srv.URL
	return b
}

func TestBybit(t *testing.T) {
	b := bybitServer(t, map[string]string{
		"BTCUSDT": `{"retCode":0,"time":1794253163147,"result":{"list":[{"symbol":"BTCUSDT","lastPrice":"74891.3","price24hPcnt":"-0.0131"}]}}`,
		"ETHUSDT": `{"retCode":0,"time":1794253163147,"result":{"list":[{"symbol":"ETHUSDT","lastPrice":"2132.5","price24hPcnt":"0.02"}]}}`,
		// TONUSDT: not listed on Bybit spot, as in real life.
	})
	got, err := b.Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "Bybit" || len(got.Quotes) != 2 || got.Quotes[0].Symbol != "BTC" || got.Quotes[1].Symbol != "ETH" {
		t.Fatalf("board = %+v", got)
	}
	// price24hPcnt is a fraction.
	if !near(got.Quotes[0].ChangePct, -1.31) || !near(got.Quotes[1].ChangePct, 2) || got.Quotes[0].Price != 74891.3 {
		t.Fatalf("quotes = %+v", got.Quotes)
	}
}

func TestBybitErrors(t *testing.T) {
	ok := `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","lastPrice":"1","price24hPcnt":"0"}]}}`
	for _, tt := range []struct {
		name string
		eth  string
		want string
	}{
		{"one coin fails", "500", "unexpected status 500"},
		{"rate limited", `{"retCode":10006,"retMsg":"Too many visits"}`, "retCode 10006"},
		{"bad percentage", `{"retCode":0,"result":{"list":[{"symbol":"ETHUSDT","lastPrice":"1","price24hPcnt":""}]}}`, "price24hPcnt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := bybitServer(t, map[string]string{"BTCUSDT": ok, "ETHUSDT": tt.eth})
			if _, err := b.Board(context.Background()); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := bybitServer(t, nil).Board(context.Background()); !errors.Is(err, errNoQuotes) {
		t.Fatalf("no coin listed: err = %v", err)
	}
}

func TestFallback(t *testing.T) {
	down := func(context.Context) (Board, error) { return Board{}, errors.New("binance down") }
	up := func(context.Context) (Board, error) { return Board{Source: "Bybit"}, nil }
	var calls atomic.Int32
	never := func(context.Context) (Board, error) { calls.Add(1); return Board{}, nil }

	if got, err := Fallback(down, up, never)(context.Background()); err != nil || got.Source != "Bybit" || calls.Load() != 0 {
		t.Fatalf("got %+v, %v; later fetchers called %d times", got, err, calls.Load())
	}
	other := func(context.Context) (Board, error) { return Board{}, errors.New("bybit down") }
	_, err := Fallback(down, other)(context.Background())
	if err == nil || !strings.Contains(err.Error(), "binance down") || !strings.Contains(err.Error(), "bybit down") {
		t.Fatalf("all down: err = %v", err)
	}
	// Once the request's time is up, the next exchange isn't tried.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fallback(down, never)(ctx); err == nil || calls.Load() != 0 {
		t.Fatalf("after cancel: err = %v, calls = %d", err, calls.Load())
	}
}

// A hanging first exchange times out alone; the second still gets its turn.
func TestFallbackWithTimeout(t *testing.T) {
	hang := func(ctx context.Context) (Board, error) { <-ctx.Done(); return Board{}, ctx.Err() }
	up := func(ctx context.Context) (Board, error) {
		if ctx.Err() != nil {
			return Board{}, ctx.Err()
		}
		return Board{Source: "Bybit"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	got, err := Fallback(WithTimeout(hang, 50*time.Millisecond), up)(ctx)
	if err != nil || got.Source != "Bybit" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v: the first exchange's timeout didn't apply", time.Since(start))
	}
}

func moexServer(t *testing.T, index, shares string) *MOEX {
	t.Helper()
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("iss.meta") != "off" {
			t.Errorf("iss.meta = %q", r.URL.Query().Get("iss.meta"))
		}
		var body string
		switch r.URL.Path {
		case "/index/boards/SNDX/securities.json":
			body = index
		case "/shares/boards/TQBR/securities.json":
			if got := r.URL.Query().Get("securities"); got != "SBER,GAZP,NOPE" {
				t.Errorf("securities = %q", got)
			}
			body = shares
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if body == "500" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	m := NewMOEX(srv.Client(), DefaultIndex, []Security{{"SBER", "Сбербанк"}, {"GAZP", "Газпром"}, {"NOPE", "Нет такой"}})
	m.base = srv.URL
	return m
}

const moexIndex = `{"marketdata":{"columns":["SECID","CURRENTVALUE","LASTCHANGEPRC","UPDATETIME","SYSTIME"],
	"data":[["IMOEX",2471.46,0.31,"22:48:35","2026-11-09 22:48:35"]]}}`

func TestMOEX(t *testing.T) {
	// Columns in an order other than asked for, GAZP without trades yet,
	// and NOPE silently missing, as ISS does with an unknown code.
	shares := `{
		"securities":{"columns":["PREVPRICE","SECID"],"data":[[293.99,"SBER"],[114.12,"GAZP"]]},
		"marketdata":{"columns":["SYSTIME","SECID","LAST","LASTTOPREVPRICE","UPDATETIME"],
			"data":[["2026-11-10 00:05:00","SBER",295.42,0.49,"23:49:50"],["2026-11-10 00:05:00","GAZP",null,null,null]]}}`
	got, err := moexServer(t, moexIndex, shares).Board(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "Московская биржа" || got.Delay != 15*time.Minute || len(got.Quotes) != 3 {
		t.Fatalf("board = %+v", got)
	}
	imoex, sber, gazp := got.Quotes[0], got.Quotes[1], got.Quotes[2]
	if imoex.Symbol != "IMOEX" || imoex.Price != 2471.46 || imoex.ChangePct != 0.31 || imoex.Currency != "" ||
		!imoex.At.Equal(time.Date(2026, 11, 9, 19, 48, 35, 0, time.UTC)) {
		t.Fatalf("index = %+v", imoex)
	}
	// Traded at 23:49:50 and seen after midnight: that was the evening before.
	if sber.Name != "Сбербанк" || sber.Price != 295.42 || sber.ChangePct != 0.49 || sber.Currency != "RUB" ||
		!sber.At.Equal(time.Date(2026, 11, 9, 20, 49, 50, 0, time.UTC)) {
		t.Fatalf("sber = %+v (at %v)", sber, sber.At.In(moscow))
	}
	if gazp.Price != 114.12 || gazp.ChangePct != 0 || !gazp.At.IsZero() {
		t.Fatalf("gazp without trades = %+v, want the previous close", gazp)
	}
}

func TestMOEXErrors(t *testing.T) {
	shares := `{"securities":{"columns":[],"data":[]},"marketdata":{"columns":[],"data":[]}}`
	if _, err := moexServer(t, "500", shares).Board(context.Background()); err == nil || !strings.Contains(err.Error(), "index") {
		t.Fatalf("index down: err = %v", err)
	}
	if _, err := moexServer(t, moexIndex, "500").Board(context.Background()); err == nil || !strings.Contains(err.Error(), "shares") {
		t.Fatalf("shares down: err = %v", err)
	}
	empty := `{"marketdata":{"columns":["SECID"],"data":[]}}`
	if _, err := moexServer(t, empty, shares).Board(context.Background()); !errors.Is(err, errNoQuotes) {
		t.Fatalf("nothing quoted: err = %v", err)
	}
}
