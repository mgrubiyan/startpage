package markets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// Binance reads 24-hour tickers from Binance's market-data-only host, which
// serves public data without an API key.
type Binance struct {
	http  *http.Client
	url   string
	coins []Coin
}

// NewBinance returns a client for coins.
func NewBinance(hc *http.Client, coins []Coin) *Binance {
	return &Binance{http: hc, url: "https://data-api.binance.vision/api/v3/ticker/24hr", coins: coins}
}

type binanceTicker struct {
	Symbol    string `json:"symbol"`
	OpenPrice string `json:"openPrice"`
	LastPrice string `json:"lastPrice"`
	CloseTime int64  `json:"closeTime"` // ms; the 24h window ends now
}

// Board fetches every coin in one request. Binance rejects the whole request
// if any symbol is unknown, which is why the coin list is fixed in code.
func (b *Binance) Board(ctx context.Context) (Board, error) {
	pairs := make([]string, len(b.coins))
	for i, c := range b.coins {
		pairs[i] = c.pair()
	}
	symbols, err := json.Marshal(pairs)
	if err != nil {
		return Board{}, err
	}
	q := url.Values{"symbols": {string(symbols)}, "type": {"MINI"}}
	var tickers []binanceTicker
	if err := fetch.JSON(ctx, b.http, b.url+"?"+q.Encode(), &tickers); err != nil {
		return Board{}, fmt.Errorf("binance: %w", err)
	}
	bySymbol := make(map[string]binanceTicker, len(tickers))
	for _, t := range tickers {
		bySymbol[t.Symbol] = t
	}
	board := Board{Source: "Binance"}
	for _, c := range b.coins {
		t, ok := bySymbol[c.pair()]
		if !ok {
			continue
		}
		last, err := parsePrice("lastPrice", t.LastPrice)
		if err != nil {
			return Board{}, fmt.Errorf("binance %s: %w", t.Symbol, err)
		}
		// MINI tickers carry no percentage; the open of the 24h window gives it.
		open, err := parsePrice("openPrice", t.OpenPrice)
		if err != nil {
			return Board{}, fmt.Errorf("binance %s: %w", t.Symbol, err)
		}
		board.Quotes = append(board.Quotes, Quote{
			Symbol: c.Symbol, Name: c.Name, Price: last, ChangePct: changePct(last, open),
			Currency: "USDT", At: unixMilli(t.CloseTime),
		})
	}
	if len(board.Quotes) == 0 {
		return Board{}, fmt.Errorf("binance: %w", errNoQuotes)
	}
	return board, nil
}

// Bybit reads spot tickers from Bybit's public v5 API, one request per coin:
// without a symbol it would send every spot market, far more than needed.
type Bybit struct {
	http  *http.Client
	url   string
	coins []Coin
}

// NewBybit returns a client for coins.
func NewBybit(hc *http.Client, coins []Coin) *Bybit {
	return &Bybit{http: hc, url: "https://api.bybit.com/v5/market/tickers", coins: coins}
}

type bybitResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Time    int64  `json:"time"` // ms
	Result  struct {
		List []struct {
			Symbol       string `json:"symbol"`
			LastPrice    string `json:"lastPrice"`
			Price24hPcnt string `json:"price24hPcnt"` // a fraction: "-0.0131" is −1.31%
		} `json:"list"`
	} `json:"result"`
}

// bybitUnsupported is Bybit's retCode for a symbol it doesn't list (e.g. TONUSDT on spot).
const bybitUnsupported = 10001

// errUnlisted marks a coin the exchange doesn't trade; the others still count.
var errUnlisted = errors.New("not listed")

// Board fetches the coins concurrently. A coin Bybit doesn't list is left out;
// any other failure fails the board, so a partial answer isn't served as whole.
func (b *Bybit) Board(ctx context.Context) (Board, error) {
	quotes := make([]Quote, len(b.coins))
	errs := make([]error, len(b.coins))
	var wg sync.WaitGroup
	for i, c := range b.coins {
		wg.Go(func() { quotes[i], errs[i] = b.quote(ctx, c) })
	}
	wg.Wait()

	board := Board{Source: "Bybit"}
	for i, q := range quotes {
		switch {
		case errors.Is(errs[i], errUnlisted):
		case errs[i] != nil:
			return Board{}, errs[i]
		default:
			board.Quotes = append(board.Quotes, q)
		}
	}
	if len(board.Quotes) == 0 {
		return Board{}, fmt.Errorf("bybit: %w", errNoQuotes)
	}
	return board, nil
}

func (b *Bybit) quote(ctx context.Context, c Coin) (Quote, error) {
	q := url.Values{"category": {"spot"}, "symbol": {c.pair()}}
	var resp bybitResponse
	if err := fetch.JSON(ctx, b.http, b.url+"?"+q.Encode(), &resp); err != nil {
		return Quote{}, fmt.Errorf("bybit %s: %w", c.pair(), err)
	}
	switch {
	case resp.RetCode == bybitUnsupported:
		return Quote{}, fmt.Errorf("bybit %s: %w", c.pair(), errUnlisted)
	case resp.RetCode != 0:
		return Quote{}, fmt.Errorf("bybit %s: retCode %d %q", c.pair(), resp.RetCode, resp.RetMsg)
	case len(resp.Result.List) == 0 || resp.Result.List[0].Symbol != c.pair():
		return Quote{}, fmt.Errorf("bybit %s: %w", c.pair(), errUnlisted)
	}
	t := resp.Result.List[0]
	last, err := parsePrice("lastPrice", t.LastPrice)
	if err != nil {
		return Quote{}, fmt.Errorf("bybit %s: %w", c.pair(), err)
	}
	pct, err := strconv.ParseFloat(t.Price24hPcnt, 64)
	if err != nil {
		return Quote{}, fmt.Errorf("bybit %s: price24hPcnt %q is not a number", c.pair(), t.Price24hPcnt)
	}
	return Quote{
		Symbol: c.Symbol, Name: c.Name, Price: last, ChangePct: pct * 100,
		Currency: "USDT", At: unixMilli(resp.Time),
	}, nil
}

func unixMilli(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
