package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/markets"
)

// MarketSource returns one exchange's board for the comma-separated symbols
// in key, as the user listed them. *cache.Cache[markets.Board] implements it.
type MarketSource interface {
	Get(ctx context.Context, key string) (cache.Result[markets.Board], error)
}

// demoMarketsReason is why the public demo shows no quotes. MOEX requires a
// separate agreement to display its data to the public, and Binance has no
// policy that clearly allows it; a personal instance behind a login is the
// individual use both serve for free.
const demoMarketsReason = "В публичном демо котировки отключены: Мосбиржа требует договор для публичного показа своих данных, а Binance такой показ явно не разрешает. На личной странице за паролем они работают."

type quoteDTO struct {
	Symbol   string     `json:"symbol"`
	Name     string     `json:"name"`
	Price    float64    `json:"price"`
	Change   float64    `json:"change"` // percent
	Currency string     `json:"currency,omitempty"`
	At       *time.Time `json:"at,omitempty"`
}

type boardDTO struct {
	Source       string     `json:"source,omitempty"`
	DelayMinutes int        `json:"delayMinutes,omitempty"`
	Quotes       []quoteDTO `json:"quotes"`
	FetchedAt    *time.Time `json:"fetchedAt,omitempty"`
	Stale        bool       `json:"stale"`
	// Missing are the symbols this exchange didn't quote: a typo, or a coin
	// only the other exchange lists while it stands in.
	Missing []string `json:"missing,omitempty"`
	// Error is set instead of quotes when this exchange is unavailable;
	// the other board is still served.
	Error string `json:"error,omitempty"`
}

type marketsDTO struct {
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
	Crypto    *boardDTO `json:"crypto,omitempty"`
	Stocks    *boardDTO `json:"stocks,omitempty"`
}

func (a *api) getMarkets(w http.ResponseWriter, r *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, marketsDTO{Reason: demoMarketsReason})
		return
	}
	if a.crypto == nil && a.stocks == nil {
		writeJSON(w, http.StatusOK, marketsDTO{Reason: "Котировки не настроены."})
		return
	}
	s, err := a.store.Settings(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// No coins listed means no crypto board; the shares board keeps the index.
	cryptoSrc, stocksSrc := a.crypto, a.stocks
	if len(s.Crypto) == 0 {
		cryptoSrc = nil
	}
	// Both exchanges at once: a slow one shouldn't hold up the other.
	var (
		wg             sync.WaitGroup
		crypto, stocks *boardDTO
		failed         [2]bool
	)
	load := func(src MarketSource, name string, symbols []string, out **boardDTO, fail *bool) {
		if src == nil {
			return
		}
		wg.Go(func() { *out, *fail = a.board(r, src, name, strings.Join(symbols, ",")) })
	}
	load(cryptoSrc, "crypto", s.Crypto, &crypto, &failed[0])
	load(stocksSrc, "stocks", s.Shares, &stocks, &failed[1])
	wg.Wait()
	if r.Context().Err() != nil {
		return // client went away
	}
	if cryptoSrc == nil && stocksSrc == nil {
		writeJSON(w, http.StatusOK, marketsDTO{Available: true})
		return
	}
	if (cryptoSrc == nil || failed[0]) && (stocksSrc == nil || failed[1]) {
		writeError(w, http.StatusBadGateway, "markets unavailable")
		return
	}
	writeJSON(w, http.StatusOK, marketsDTO{Available: true, Crypto: crypto, Stocks: stocks})
}

// board loads one exchange. A failure is logged and reported in the board
// itself, so the page can still show the other exchange.
func (a *api) board(r *http.Request, src MarketSource, name, symbols string) (*boardDTO, bool) {
	res, err := src.Get(r.Context(), symbols)
	if err != nil {
		if r.Context().Err() == nil {
			a.logger.WarnContext(r.Context(), "upstream failed", "upstream", "markets/"+name, "err", err)
		}
		return &boardDTO{Quotes: []quoteDTO{}, Error: "недоступно"}, true
	}
	b := res.Value
	out := &boardDTO{
		Source:       b.Source,
		DelayMinutes: int(b.Delay / time.Minute),
		Quotes:       make([]quoteDTO, 0, len(b.Quotes)),
		FetchedAt:    &res.FetchedAt,
		Stale:        res.Stale,
		Missing:      b.Missing,
	}
	for _, q := range b.Quotes {
		dto := quoteDTO{Symbol: q.Symbol, Name: q.Name, Price: q.Price, Change: q.ChangePct, Currency: q.Currency}
		if !q.At.IsZero() {
			at := q.At
			dto.At = &at
		}
		out.Quotes = append(out.Quotes, dto)
	}
	return out, false
}
