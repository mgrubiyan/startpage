// Package suggest returns search-as-you-type suggestions.
//
// Google's public suggest endpoint is tried first; DuckDuckGo is the fallback.
// Both answer in the OpenSearch suggestions format: ["query", ["s1", "s2", ...], ...].
package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

// MaxResults caps the suggestions returned for one query.
const MaxResults = 8

type provider struct {
	name  string
	build func(q string) string
}

// Client queries suggestion providers in order until one succeeds.
type Client struct {
	http      *http.Client
	providers []provider
	timeout   time.Duration // per provider; zero means ProviderTimeout
}

func (c *Client) providerTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return ProviderTimeout
}

// New returns a Client with Google as primary and DuckDuckGo as fallback.
func New(hc *http.Client) *Client {
	return &Client{http: hc, providers: []provider{
		{"google", func(q string) string {
			v := url.Values{"client": {"firefox"}, "hl": {"ru"}, "ie": {"utf-8"}, "oe": {"utf-8"}, "q": {q}}
			return "https://suggestqueries.google.com/complete/search?" + v.Encode()
		}},
		{"duckduckgo", func(q string) string {
			return "https://duckduckgo.com/ac/?" + url.Values{"type": {"list"}, "q": {q}}.Encode()
		}},
	}}
}

// ProviderTimeout bounds each provider separately, so a hanging primary leaves
// time for the fallback within the caller's overall deadline.
const ProviderTimeout = 1500 * time.Millisecond

// Suggest returns up to MaxResults suggestions for q.
func (c *Client) Suggest(ctx context.Context, q string) ([]string, error) {
	var errs []error
	for _, p := range c.providers {
		pctx, cancel := context.WithTimeout(ctx, c.providerTimeout())
		out, err := c.query(pctx, p, q)
		cancel()
		if err == nil {
			return out, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", p.name, err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, fmt.Errorf("suggest: %w", errors.Join(errs...))
}

func (c *Client) query(ctx context.Context, p provider, q string) ([]string, error) {
	var raw []json.RawMessage
	if err := fetch.JSON(ctx, c.http, p.build(q), &raw); err != nil {
		return nil, err
	}
	if len(raw) < 2 {
		return nil, errors.New("unexpected response shape")
	}
	var list []string
	if err := json.Unmarshal(raw[1], &list); err != nil {
		return nil, fmt.Errorf("unexpected suggestions list: %w", err)
	}
	if len(list) > MaxResults {
		list = list[:MaxResults]
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}
