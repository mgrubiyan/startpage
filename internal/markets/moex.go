package markets

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// Security is a MOEX instrument and the name the page shows for it.
type Security struct {
	SecID string // MOEX code: SBER, IMOEX
	Name  string
}

// DefaultIndex heads the shares board: the market as a whole.
var DefaultIndex = Security{"IMOEX", "Индекс МосБиржи"}

// shareNames are short Russian names for the bigger companies; any other share
// takes the exchange's own short name (SHORTNAME).
var shareNames = map[string]string{
	"SBER": "Сбербанк", "SBERP": "Сбербанк, преф.", "GAZP": "Газпром", "LKOH": "Лукойл",
	"YDEX": "Яндекс", "ROSN": "Роснефть", "NVTK": "Новатэк", "GMKN": "Норникель",
	"TATN": "Татнефть", "VTBR": "ВТБ", "MTSS": "МТС", "MGNT": "Магнит",
	"PLZL": "Полюс", "CHMF": "Северсталь", "AFLT": "Аэрофлот", "OZON": "Ozon", "T": "Т-Технологии",
}

// Shares turns tickers, as the user listed them, into securities.
func Shares(tickers []string) []Security {
	out := make([]Security, len(tickers))
	for i, t := range tickers {
		out[i] = Security{SecID: t, Name: shareNames[t]}
	}
	return out
}

// MOEXDelay is how late the Moscow Exchange publishes share prices for free.
const MOEXDelay = 15 * time.Minute

// moscow is the exchange's clock. A fixed offset, so the binary needs no tzdata
// (Russia has had no daylight saving time since 2014).
var moscow = time.FixedZone("MSK", 3*60*60)

// MOEX reads the main share board (TQBR) and an index from the Moscow Exchange
// Information & Statistical Server, which needs no key for delayed data.
type MOEX struct {
	http   *http.Client
	base   string
	index  Security
	shares []Security
}

// NewMOEX returns a client for an index and a list of shares.
func NewMOEX(hc *http.Client, index Security, shares []Security) *MOEX {
	return &MOEX{http: hc, base: "https://iss.moex.com/iss/engines/stock/markets", index: index, shares: shares}
}

// issTable is how ISS sends a table: column names and rows of mixed values.
// Columns are looked up by name, never by position, and a column ISS doesn't
// have for this board is simply absent.
type issTable struct {
	Columns []string `json:"columns"`
	Data    [][]any  `json:"data"`
}

type issRow map[string]any

func (t issTable) rows() []issRow {
	out := make([]issRow, 0, len(t.Data))
	for _, d := range t.Data {
		r := make(issRow, len(t.Columns))
		for i, c := range t.Columns {
			if i < len(d) {
				r[c] = d[i]
			}
		}
		out = append(out, r)
	}
	return out
}

// num returns a numeric column, or false when it is null or missing.
func (r issRow) num(col string) (float64, bool) {
	v, ok := r[col].(float64)
	return v, ok
}

func (r issRow) str(col string) string {
	s, _ := r[col].(string)
	return s
}

// updatedAt joins the date of SYSTIME ("2026-11-09 22:48:21", when ISS built
// the answer) with UPDATETIME ("22:33:20", the last price change). A price
// changed late in the evening and seen after midnight belongs to the day before.
func (r issRow) updatedAt() time.Time {
	sys, err := time.ParseInLocation(time.DateTime, r.str("SYSTIME"), moscow)
	if err != nil {
		return time.Time{}
	}
	at, err := time.ParseInLocation(time.DateTime, sys.Format(time.DateOnly)+" "+r.str("UPDATETIME"), moscow)
	if err != nil {
		return time.Time{}
	}
	if at.After(sys) {
		at = at.AddDate(0, 0, -1)
	}
	return at
}

type issResponse struct {
	Securities issTable `json:"securities"`
	Marketdata issTable `json:"marketdata"`
}

// Board fetches the index and the shares concurrently, index first in the result.
// An instrument ISS doesn't know is left out; a failed request fails the board.
func (m *MOEX) Board(ctx context.Context) (Board, error) {
	var (
		wg               sync.WaitGroup
		index, shares    []Quote
		indexErr, shrErr error
	)
	wg.Go(func() { index, indexErr = m.indexQuotes(ctx) })
	wg.Go(func() { shares, shrErr = m.shareQuotes(ctx) })
	wg.Wait()
	if err := errors.Join(indexErr, shrErr); err != nil {
		return Board{}, fmt.Errorf("moex: %w", err)
	}
	quotes := slices.Concat(index, shares)
	if len(quotes) == 0 {
		return Board{}, fmt.Errorf("moex: %w", errNoQuotes)
	}
	ids := make([]string, len(m.shares))
	for i, s := range m.shares {
		ids[i] = s.SecID
	}
	return Board{Source: "Московская биржа", Delay: MOEXDelay, Quotes: quotes, Missing: missing(ids, shares)}, nil
}

func (m *MOEX) get(ctx context.Context, path string, q url.Values) (issResponse, error) {
	q.Set("iss.meta", "off")
	var resp issResponse
	err := fetch.JSON(ctx, m.http, m.base+path+"?"+q.Encode(), &resp)
	return resp, err
}

func (m *MOEX) indexQuotes(ctx context.Context) ([]Quote, error) {
	resp, err := m.get(ctx, "/index/boards/SNDX/securities.json", url.Values{
		"securities":         {m.index.SecID},
		"iss.only":           {"marketdata"},
		"marketdata.columns": {"SECID,CURRENTVALUE,LASTCHANGEPRC,UPDATETIME,SYSTIME"},
	})
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	for _, r := range resp.Marketdata.rows() {
		if r.str("SECID") != m.index.SecID {
			continue
		}
		v, ok := r.num("CURRENTVALUE")
		if !ok || v <= 0 {
			return nil, nil // not calculated yet today
		}
		pct, _ := r.num("LASTCHANGEPRC")
		return []Quote{{Symbol: m.index.SecID, Name: m.index.Name, Price: v, ChangePct: pct, At: r.updatedAt()}}, nil
	}
	return nil, nil
}

func (m *MOEX) shareQuotes(ctx context.Context) ([]Quote, error) {
	if len(m.shares) == 0 {
		return nil, nil // the index alone
	}
	ids := make([]string, len(m.shares))
	for i, s := range m.shares {
		ids[i] = s.SecID
	}
	resp, err := m.get(ctx, "/shares/boards/TQBR/securities.json", url.Values{
		"securities":         {strings.Join(ids, ",")},
		"iss.only":           {"securities,marketdata"},
		"securities.columns": {"SECID,SHORTNAME,PREVPRICE"},
		"marketdata.columns": {"SECID,LAST,LASTTOPREVPRICE,UPDATETIME,SYSTIME"},
	})
	if err != nil {
		return nil, fmt.Errorf("shares: %w", err)
	}
	prev := make(map[string]float64)
	short := make(map[string]string)
	for _, r := range resp.Securities.rows() {
		if v, ok := r.num("PREVPRICE"); ok {
			prev[r.str("SECID")] = v
		}
		short[r.str("SECID")] = r.str("SHORTNAME")
	}
	market := make(map[string]issRow)
	for _, r := range resp.Marketdata.rows() {
		market[r.str("SECID")] = r
	}
	var out []Quote
	for _, s := range m.shares {
		r, ok := market[s.SecID]
		if !ok {
			continue // unknown to ISS: it just leaves the row out
		}
		name := s.Name
		if name == "" {
			name = cmp.Or(short[s.SecID], s.SecID)
		}
		q := Quote{Symbol: s.SecID, Name: name, Currency: "RUB"}
		if last, ok := r.num("LAST"); ok && last > 0 {
			q.Price = last
			q.ChangePct, _ = r.num("LASTTOPREVPRICE")
			q.At = r.updatedAt()
		} else if p := prev[s.SecID]; p > 0 {
			// No trades yet this session: the previous close, unchanged.
			q.Price = p
		} else {
			continue
		}
		out = append(out, q)
	}
	return out, nil
}
