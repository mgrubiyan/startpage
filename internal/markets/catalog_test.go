package markets

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func symbols(ls []Listing) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Symbol + ":" + l.Name
	}
	return out
}

func TestBinanceCoins(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "MINI" {
			t.Errorf("type = %q", r.URL.Query().Get("type"))
		}
		// A body over the usual 1 MiB limit: the whole exchange is that big.
		pad := strings.Repeat(`{"symbol":"PADBTC","quoteVolume":"1","count":1},`, 30000)
		_, _ = w.Write([]byte(`[` + pad + `
			{"symbol":"ETHUSDT","quoteVolume":"500.5","count":10},
			{"symbol":"BTCUSDT","quoteVolume":"1600","count":20},
			{"symbol":"BCCUSDT","quoteVolume":"0","count":0},
			{"symbol":"ETHBTC","quoteVolume":"9999","count":5},
			{"symbol":"NEWUSDT","quoteVolume":"3","count":2}]`))
	})
	got, err := binanceCoins(context.Background(), srv.Client(), srv.URL+"/?type=MINI")
	if err != nil {
		t.Fatal(err)
	}
	// Most traded first; dead pairs and non-USDT ones left out; unknown names are the symbol.
	want := []string{"BTC:Bitcoin", "ETH:Ethereum", "NEW:NEW"}
	if !reflect.DeepEqual(symbols(got), want) {
		t.Fatalf("got %q, want %q", symbols(got), want)
	}
}

func TestMOEXShares(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shares/boards/TQBR/securities.json" || r.URL.Query().Get("securities") != "" {
			t.Errorf("unexpected request %s", r.URL)
		}
		_, _ = w.Write([]byte(`{
			"securities":{"columns":["SHORTNAME","SECID"],"data":[["АбрауДюрсо","ABRD"],["Сбербанк","SBER"],["ГАЗПРОМ ао","GAZP"],["Аптеки36и6","APTK"]]},
			"marketdata":{"columns":["SECID","VALTODAY"],"data":[["ABRD",0],["SBER",1500],["GAZP",2100],["APTK",null]]}}`))
	})
	m := NewMOEX(srv.Client(), DefaultIndex, nil)
	m.base = srv.URL
	got, err := m.catalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// By turnover, then by ticker; known names win over the exchange's short ones.
	want := []string{"GAZP:Газпром", "SBER:Сбербанк", "ABRD:АбрауДюрсо", "APTK:Аптеки36и6"}
	if !reflect.DeepEqual(symbols(got), want) {
		t.Fatalf("got %q, want %q", symbols(got), want)
	}
}
