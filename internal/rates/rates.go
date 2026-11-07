// Package rates fetches official Central Bank of Russia exchange rates
// via the cbr-xml-daily.ru JSON mirror.
package rates

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// Rate is the ruble price of one unit of a currency today and on the previous publication.
type Rate struct {
	Value    float64
	Previous float64
}

// Rates are the currencies shown on the start page.
type Rates struct {
	Date time.Time
	USD  Rate
	EUR  Rate
}

// Client talks to the CBR daily JSON feed.
type Client struct {
	http *http.Client
	url  string
}

// New returns a Client for the public feed.
func New(hc *http.Client) *Client {
	return &Client{http: hc, url: "https://www.cbr-xml-daily.ru/daily_json.js"}
}

type dailyResponse struct {
	Date   time.Time `json:"Date"`
	Valute map[string]struct {
		Nominal  float64 `json:"Nominal"`
		Value    float64 `json:"Value"`
		Previous float64 `json:"Previous"`
	} `json:"Valute"`
}

// Latest returns today's USD and EUR rates.
func (c *Client) Latest(ctx context.Context) (Rates, error) {
	var d dailyResponse
	if err := fetch.JSON(ctx, c.http, c.url, &d); err != nil {
		return Rates{}, fmt.Errorf("cbr rates: %w", err)
	}
	pick := func(code string) (Rate, error) {
		v, ok := d.Valute[code]
		if !ok || v.Value <= 0 {
			return Rate{}, fmt.Errorf("cbr rates: %s missing from feed", code)
		}
		// Rates are quoted per Nominal units (e.g. 100 for some currencies); normalize to one unit.
		n := v.Nominal
		if n <= 0 {
			n = 1
		}
		return Rate{Value: v.Value / n, Previous: v.Previous / n}, nil
	}
	usd, err := pick("USD")
	if err != nil {
		return Rates{}, err
	}
	eur, err := pick("EUR")
	if err != nil {
		return Rates{}, err
	}
	return Rates{Date: d.Date, USD: usd, EUR: eur}, nil
}
