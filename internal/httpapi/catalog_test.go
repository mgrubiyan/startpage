package httpapi

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/markets"
)

// fakeCatalog serves a list per board; a board it doesn't have fails.
type fakeCatalog map[string][]markets.Listing

func (f fakeCatalog) Get(_ context.Context, board string) (cache.Result[[]markets.Listing], error) {
	l, ok := f[board]
	if !ok {
		return cache.Result[[]markets.Listing]{}, errors.New("upstream down")
	}
	return cache.Result[[]markets.Listing]{Value: l, FetchedAt: testNow}, nil
}

func TestMarketCatalog(t *testing.T) {
	env := newTestEnv(t, func(d *Deps) {
		d.Catalog = fakeCatalog{"crypto": {
			{Symbol: "BTC", Name: "Bitcoin"},
			{Symbol: "A", Name: "A"},       // one letter: the settings refuse it
			{Symbol: "币安人生", Name: "币安人生"}, // nor this
			{Symbol: "1000SATS", Name: "1000SATS"},
		}}
	})
	got := decode[catalogDTO](t, env.do(t, "GET", "/api/markets/catalog", ""))
	if !got.Available || got.Crypto == nil || got.Shares == nil {
		t.Fatalf("got %+v", got)
	}
	want := []listingDTO{{"BTC", "Bitcoin"}, {"1000SATS", "1000SATS"}}
	if !reflect.DeepEqual(got.Crypto.Items, want) || got.Crypto.Error != "" {
		t.Errorf("crypto = %+v", got.Crypto)
	}
	// MOEX down: its list is empty with an error, the coins still come.
	if len(got.Shares.Items) != 0 || got.Shares.Error == "" {
		t.Errorf("shares = %+v", got.Shares)
	}
}

// The picker saves one list at a time: what a PUT leaves out stays as it was.
func TestSettingsPatch(t *testing.T) {
	env := newTestEnv(t)
	before := decode[settingsDTO](t, env.do(t, "GET", "/api/settings", ""))
	got := decode[settingsDTO](t, env.do(t, "PUT", "/api/settings", `{"crypto":["doge"]}`))
	if !reflect.DeepEqual(got.Crypto, []string{"DOGE"}) || got.City != before.City ||
		got.SearchEngine != before.SearchEngine || !reflect.DeepEqual(got.Shares, before.Shares) {
		t.Fatalf("before %+v, after a crypto-only PUT %+v", before, got)
	}
	got = decode[settingsDTO](t, env.do(t, "PUT", "/api/settings", `{"shares":["vtbr"]}`))
	if !reflect.DeepEqual(got.Crypto, []string{"DOGE"}) || !reflect.DeepEqual(got.Shares, []string{"VTBR"}) {
		t.Fatalf("after a shares-only PUT %+v", got)
	}
	if rec := env.do(t, "PUT", "/api/settings", `{"searchEngine":"nope"}`); rec.Code != 400 {
		t.Fatalf("unknown engine: %d", rec.Code)
	}
}

func TestMarketCatalogOff(t *testing.T) {
	for name, env := range map[string]*testEnv{"demo": demoEnv(t), "no catalog": newTestEnv(t)} {
		rec := env.do(t, "GET", "/api/markets/catalog", "")
		if got := decode[catalogDTO](t, rec); got.Available || got.Crypto != nil {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}
