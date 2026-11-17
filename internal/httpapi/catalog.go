package httpapi

import (
	"context"
	"net/http"
	"regexp"
	"sync"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/markets"
)

// CatalogSource lists what an exchange offers, keyed by board: "crypto" or
// "shares". *cache.Cache[[]markets.Listing] implements it.
type CatalogSource interface {
	Get(ctx context.Context, board string) (cache.Result[[]markets.Listing], error)
}

type listingDTO struct {
	Symbol string `json:"symbol"`
	Name   string `json:"name"`
}

type catalogBoardDTO struct {
	Items []listingDTO `json:"items"`
	// Error is set when the exchange's list couldn't be loaded; the page then
	// still takes a ticker typed in by hand.
	Error string `json:"error,omitempty"`
}

type catalogDTO struct {
	Available bool             `json:"available"`
	Crypto    *catalogBoardDTO `json:"crypto,omitempty"`
	Shares    *catalogBoardDTO `json:"shares,omitempty"`
}

// getMarketCatalog lists the coins and shares the settings can name, most
// traded first, for the picker in the markets panel. Only symbols the
// settings would accept are listed.
func (a *api) getMarketCatalog(w http.ResponseWriter, r *http.Request) {
	if a.demo || a.catalog == nil {
		writeJSON(w, http.StatusOK, catalogDTO{})
		return
	}
	var (
		wg             sync.WaitGroup
		crypto, shares *catalogBoardDTO
	)
	wg.Go(func() { crypto = a.catalogBoard(r, "crypto", coinSymbol) })
	wg.Go(func() { shares = a.catalogBoard(r, "shares", shareTicker) })
	wg.Wait()
	if r.Context().Err() != nil {
		return // client went away
	}
	writeJSON(w, http.StatusOK, catalogDTO{Available: true, Crypto: crypto, Shares: shares})
}

func (a *api) catalogBoard(r *http.Request, board string, valid *regexp.Regexp) *catalogBoardDTO {
	res, err := a.catalog.Get(r.Context(), board)
	if err != nil {
		if r.Context().Err() == nil {
			a.logger.WarnContext(r.Context(), "upstream failed", "upstream", "catalog/"+board, "err", err)
		}
		return &catalogBoardDTO{Items: []listingDTO{}, Error: "список недоступен"}
	}
	out := &catalogBoardDTO{Items: make([]listingDTO, 0, len(res.Value))}
	for _, l := range res.Value {
		if valid.MatchString(l.Symbol) {
			out.Items = append(out.Items, listingDTO{Symbol: l.Symbol, Name: l.Name})
		}
	}
	return out
}
