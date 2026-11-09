package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/markets"
)

// MarketSource returns one exchange's board; the key is ignored.
// *cache.Cache[markets.Board] implements it.
type MarketSource interface {
	Get(ctx context.Context, key string) (cache.Result[markets.Board], error)
}

// demoMarketsReason is why the public demo shows no quotes. MOEX requires a
// separate agreement to display its data to the public, and Binance has no
// policy that clearly allows it; a personal instance behind a login is the
// individual use both serve for free.
const demoMarketsReason = "В публичном демо котировки отключены: биржи разрешают бесплатно показывать их только для личного пользования."

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
	// Both exchanges at once: a slow one shouldn't hold up the other.
	var (
		wg             sync.WaitGroup
		crypto, stocks *boardDTO
		failed         [2]bool
	)
	load := func(src MarketSource, name string, out **boardDTO, fail *bool) {
		if src == nil {
			return
		}
		wg.Go(func() { *out, *fail = a.board(r, src, name) })
	}
	load(a.crypto, "crypto", &crypto, &failed[0])
	load(a.stocks, "stocks", &stocks, &failed[1])
	wg.Wait()
	if r.Context().Err() != nil {
		return // client went away
	}
	if (a.crypto == nil || failed[0]) && (a.stocks == nil || failed[1]) {
		writeError(w, http.StatusBadGateway, "markets unavailable")
		return
	}
	writeJSON(w, http.StatusOK, marketsDTO{Available: true, Crypto: crypto, Stocks: stocks})
}

// board loads one exchange. A failure is logged and reported in the board
// itself, so the page can still show the other exchange.
func (a *api) board(r *http.Request, src MarketSource, name string) (*boardDTO, bool) {
	res, err := src.Get(r.Context(), "")
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
