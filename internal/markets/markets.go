// Package markets fetches quotes from public, keyless exchange APIs: crypto
// from Binance (with Bybit as a fallback) and Russian shares from the Moscow
// Exchange (MOEX ISS).
package markets

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Quote is the latest price of one instrument.
type Quote struct {
	Symbol string // as shown on the page: BTC, SBER, IMOEX
	Name   string
	Price  float64
	// ChangePct is the change in percent: over the last 24 hours for crypto,
	// since the previous session's close for MOEX.
	ChangePct float64
	// Currency of Price: "USDT", "RUB", or "" for an index, which is in points.
	Currency string
	// At is when Price was set by the exchange; zero if the exchange doesn't say.
	At time.Time
}

// Board is a group of quotes from one exchange.
type Board struct {
	Source string // the exchange that answered, e.g. "Binance"
	// Delay is how far behind real time the exchange publishes this data for free.
	Delay  time.Duration
	Quotes []Quote
}

// Fetcher loads a Board.
type Fetcher func(ctx context.Context) (Board, error)

// Fallback tries each fetcher in order and returns the first Board it gets.
// An exchange can be down, rate-limit the server, or block its region; the
// next one keeps the widget alive. All errors are returned if every one fails.
func Fallback(fetchers ...Fetcher) Fetcher {
	return func(ctx context.Context) (Board, error) {
		errs := make([]error, 0, len(fetchers))
		for _, f := range fetchers {
			b, err := f(ctx)
			if err == nil {
				return b, nil
			}
			errs = append(errs, err)
			if ctx.Err() != nil {
				break // out of time: the rest would fail the same way
			}
		}
		return Board{}, errors.Join(errs...)
	}
}

// WithTimeout bounds one fetcher, so that in a Fallback a hanging exchange
// leaves time for the next one instead of using up the whole request.
func WithTimeout(f Fetcher, d time.Duration) Fetcher {
	return func(ctx context.Context) (Board, error) {
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return f(ctx)
	}
}

// Coin is a cryptocurrency quoted against USDT.
type Coin struct {
	Symbol string // BTC
	Name   string // Bitcoin
}

// pair is the coin's USDT market, named the same on Binance and Bybit.
func (c Coin) pair() string { return c.Symbol + "USDT" }

// DefaultCoins are the coins the page shows.
var DefaultCoins = []Coin{
	{"BTC", "Bitcoin"},
	{"ETH", "Ethereum"},
	{"SOL", "Solana"},
	{"TON", "Toncoin"},
}

// errNoQuotes means the exchange answered but quoted none of the requested instruments.
var errNoQuotes = errors.New("none of the requested instruments were quoted")

func changePct(price, base float64) float64 {
	if base == 0 {
		return 0
	}
	return (price - base) / base * 100
}

// parsePrice reads a price the exchange sent as a string.
func parsePrice(field, s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s %q is not a price", field, s)
	}
	return v, nil
}
