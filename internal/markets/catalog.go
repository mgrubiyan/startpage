package markets

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// Listing is one thing the markets panel can be set to show.
type Listing struct {
	Symbol string // BTC, SBER
	Name   string // Bitcoin, Сбербанк
}

// binanceTicker24h is Binance's 24h ticker on its public market-data host, for
// the quotes and for the list of coins alike.
const binanceTicker24h = "https://data-api.binance.vision/api/v3/ticker/24hr"

// catalogBodyLimit fits Binance's ticker for every pair (about 1 MiB in 2026)
// with room to grow.
const catalogBodyLimit = 4 << 20

// BinanceCoins lists the coins that trade against USDT on Binance, most
// traded first, for picking the crypto board. Pairs with no trades in a day
// (delisted ones linger in the ticker) are left out.
func BinanceCoins(ctx context.Context, hc *http.Client) ([]Listing, error) {
	return binanceCoins(ctx, hc, binanceTicker24h+"?type=MINI")
}

func binanceCoins(ctx context.Context, hc *http.Client, rawURL string) ([]Listing, error) {
	var tickers []struct {
		Symbol      string `json:"symbol"`
		QuoteVolume string `json:"quoteVolume"`
		Count       int64  `json:"count"`
	}
	if err := fetch.JSONLimit(ctx, hc, rawURL, &tickers, catalogBodyLimit); err != nil {
		return nil, fmt.Errorf("binance catalog: %w", err)
	}
	type ranked struct {
		Listing
		volume float64
	}
	coins := make([]ranked, 0, len(tickers)/4)
	for _, t := range tickers {
		base, ok := strings.CutSuffix(t.Symbol, "USDT")
		if !ok || base == "" || t.Count == 0 {
			continue
		}
		v, _ := strconv.ParseFloat(t.QuoteVolume, 64)
		coins = append(coins, ranked{Listing{Symbol: base, Name: coinName(base)}, v})
	}
	if len(coins) == 0 {
		return nil, fmt.Errorf("binance catalog: %w", errNoQuotes)
	}
	sort.SliceStable(coins, func(i, j int) bool { return coins[i].volume > coins[j].volume })
	out := make([]Listing, len(coins))
	for i, c := range coins {
		out[i] = c.Listing
	}
	return out, nil
}

// coinName is the known name, or the symbol itself: tickers carry no names.
func coinName(symbol string) string {
	if n := coinNames[symbol]; n != "" {
		return n
	}
	return symbol
}

// MOEXShares lists the shares on the main board (TQBR), most traded today
// first, for picking the shares board. Before the session opens every
// turnover is zero, and the list falls back to ticker order.
func MOEXShares(ctx context.Context, hc *http.Client) ([]Listing, error) {
	return NewMOEX(hc, DefaultIndex, nil).catalog(ctx)
}

func (m *MOEX) catalog(ctx context.Context) ([]Listing, error) {
	resp, err := m.get(ctx, "/shares/boards/TQBR/securities.json", url.Values{
		"iss.only":           {"securities,marketdata"},
		"securities.columns": {"SECID,SHORTNAME"},
		"marketdata.columns": {"SECID,VALTODAY"},
	})
	if err != nil {
		return nil, fmt.Errorf("moex catalog: %w", err)
	}
	turnover := make(map[string]float64)
	for _, r := range resp.Marketdata.rows() {
		v, _ := r.num("VALTODAY")
		turnover[r.str("SECID")] = v
	}
	out := make([]Listing, 0, len(resp.Securities.Data))
	for _, r := range resp.Securities.rows() {
		id := r.str("SECID")
		if id == "" {
			continue
		}
		name := shareNames[id]
		if name == "" {
			name = r.str("SHORTNAME")
		}
		out = append(out, Listing{Symbol: id, Name: name})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("moex catalog: %w", errNoQuotes)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := turnover[out[i].Symbol], turnover[out[j].Symbol]
		if a != b {
			return a > b
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}
