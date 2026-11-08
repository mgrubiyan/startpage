// Package art picks a painting of the day from the Cleveland Museum of Art Open
// Access collection (public domain, CC0) for the start page background.
//
// The server downloads the image and serves it itself, so the browser never
// talks to a third party. Every URL taken from the museum's JSON is checked
// against a fixed host before it is fetched or shown.
package art

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mgrubiyan/startpage/internal/fetch"
)

const (
	pageSize = 50 // records looked at per attempt; about 30% of paintings pass the filter
	attempts = 3

	minWidth = 2400 // px: sharp on a large screen
	// Landscape, but not a scroll or a manuscript leaf: under object-fit: cover a
	// 16:9 screen crops away most of a portrait or of anything wider than 2:1.
	minAspect   = 1.2
	maxAspect   = 2.0
	maxFileSize = 6 << 20
	// maxImageBytes caps the download itself, in case the listed size is wrong.
	maxImageBytes = 8 << 20
)

// Artwork is one painting and its JPEG image. Image is shared between
// requests and must not be modified.
type Artwork struct {
	ID     int
	Title  string
	Artist string
	Date   string
	URL    string // the museum's page about the work, or "" if it had none we trust
	Image  []byte
}

// Client talks to the Open Access API and its image CDN.
type Client struct {
	http    *http.Client
	api     string // artworks endpoint
	apiHost string
	cdnHost string // the only host images are downloaded from
}

// New returns a Client for the public API. It copies hc so it can refuse
// redirects that leave the museum's own hosts.
func New(hc *http.Client) *Client {
	return newClient(hc, "https://openaccess-api.clevelandart.org/api/artworks/", "openaccess-cdn.clevelandart.org")
}

func newClient(hc *http.Client, api, cdnHost string) *Client {
	c := &Client{api: api, cdnHost: cdnHost}
	if u, err := url.Parse(api); err == nil {
		c.apiHost = u.Host
	}
	cp := *hc
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !trusted(req.URL, c.apiHost, c.cdnHost) {
			return http.ErrUseLastResponse // surfaces as a non-200 status
		}
		return nil
	}
	c.http = &cp
	return c
}

type listResponse struct {
	Info struct {
		Total int `json:"total"`
	} `json:"info"`
	Data []record `json:"data"`
}

type record struct {
	ID           int    `json:"id"`
	Title        string `json:"title"`
	CreationDate string `json:"creation_date"`
	URL          string `json:"url"`
	Creators     []struct {
		Description string `json:"description"`
	} `json:"creators"`
	Images struct {
		Print *imageInfo `json:"print"`
	} `json:"images"`
}

type imageInfo struct {
	URL      string  `json:"url"`
	Width    flexInt `json:"width"`
	Height   flexInt `json:"height"`
	FileSize flexInt `json:"filesize"`
}

// flexInt reads the API's sizes, which arrive as strings and are sometimes
// empty. Anything that isn't a whole number reads as 0, which the filter
// rejects, so one odd record can't fail the whole page.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	v, err := strconv.Atoi(strings.Trim(string(b), `"`))
	if err != nil {
		v = 0
	}
	*n = flexInt(v)
	return nil
}

// departments the paintings come from. The museum files manuscript leaves,
// scrolls and album pages under "Painting" too; these three hold easel
// paintings, about 800 of them under CC0.
var departments = []string{
	"European Painting and Sculpture",
	"Modern European Painting and Sculpture",
	"American Painting and Sculpture",
}

// Pick returns the painting for day (any string, e.g. "2026-11-08"). The same
// day gives the same painting while the collection stays the same.
func (c *Client) Pick(ctx context.Context, day string) (Artwork, error) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(day))
	sum := int(h.Sum32() >> 1) // non-negative on 32-bit platforms too
	dept := departments[sum%len(departments)]

	first, err := c.list(ctx, dept, 0, 1, "id")
	if err != nil {
		return Artwork{}, err
	}
	total := first.Info.Total
	if total <= 0 {
		return Artwork{}, fmt.Errorf("art: no paintings in %q", dept)
	}
	seed := sum / len(departments) % total

	err = errors.New("art: no suitable painting found")
	for i := range attempts {
		page, listErr := c.list(ctx, dept, (seed+i*pageSize)%total, pageSize, "id,title,creators,creation_date,images,url")
		if listErr != nil {
			return Artwork{}, listErr
		}
		var usable []record
		for _, r := range page.Data {
			if c.usable(r) {
				usable = append(usable, r)
			}
		}
		if len(usable) == 0 {
			continue
		}
		r := usable[seed%len(usable)]
		img, imgErr := c.image(ctx, r.Images.Print.URL)
		if imgErr != nil {
			if ctx.Err() != nil {
				return Artwork{}, imgErr
			}
			err = imgErr // try the next page
			continue
		}
		return r.artwork(img), nil
	}
	return Artwork{}, err
}

func (c *Client) list(ctx context.Context, dept string, skip, limit int, fields string) (listResponse, error) {
	q := url.Values{
		"cc0":        {"1"},
		"has_image":  {"1"},
		"type":       {"Painting"},
		"department": {dept},
		"skip":       {strconv.Itoa(skip)},
		"limit":      {strconv.Itoa(limit)},
		"fields":     {fields},
	}
	var resp listResponse
	if err := fetch.JSON(ctx, c.http, c.api+"?"+q.Encode(), &resp); err != nil {
		return listResponse{}, fmt.Errorf("art: %w", err)
	}
	return resp, nil
}

// usable keeps large landscape paintings whose image comes from the museum's CDN.
func (c *Client) usable(r record) bool {
	p := r.Images.Print
	if p == nil || p.Height <= 0 || p.FileSize <= 0 {
		return false
	}
	u, err := url.Parse(p.URL)
	if err != nil || !trusted(u, c.cdnHost) {
		return false
	}
	return p.Width >= minWidth &&
		float64(p.Width) >= minAspect*float64(p.Height) &&
		float64(p.Width) <= maxAspect*float64(p.Height) &&
		p.FileSize <= maxFileSize
}

// trusted reports whether u is a plain https URL on one of hosts.
func trusted(u *url.URL, hosts ...string) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	for _, h := range hosts {
		if u.Host == h {
			return true
		}
	}
	return false
}

func (c *Client) image(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("art: build request: %w", err)
	}
	req.Header.Set("User-Agent", fetch.UserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("art: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("art: %w", &fetch.StatusError{URL: req.URL.Redacted(), Code: resp.StatusCode})
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("art: read %s: %w", req.URL.Redacted(), err)
	}
	if len(b) > maxImageBytes {
		return nil, fmt.Errorf("art: %s is over %d bytes", req.URL.Redacted(), maxImageBytes)
	}
	// Sniffed rather than taken from the response, because it is served on as image/jpeg.
	if ct := http.DetectContentType(b); ct != "image/jpeg" {
		return nil, fmt.Errorf("art: %s is %s, not a JPEG", req.URL.Redacted(), ct)
	}
	return b, nil
}

func (r record) artwork(img []byte) Artwork {
	a := Artwork{ID: r.ID, Title: r.Title, Date: r.CreationDate, Image: img}
	if len(r.Creators) > 0 {
		// "Claude Monet (French, 1840–1926)" -> "Claude Monet"
		name, _, _ := strings.Cut(r.Creators[0].Description, " (")
		a.Artist = strings.TrimSpace(name)
	}
	if u, err := url.Parse(r.URL); err == nil && trusted(u, "clevelandart.org", "www.clevelandart.org") {
		a.URL = u.String()
	}
	return a
}
