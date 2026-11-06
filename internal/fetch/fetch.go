// Package fetch performs bounded JSON GET requests to third-party APIs.
package fetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// UserAgent identifies this project to upstream APIs; deliberately carries no personal data.
const UserAgent = "startpage/1.0 (+https://github.com/mgrubiyan/startpage)"

const maxBodyBytes = 1 << 20

// StatusError is returned for non-200 upstream responses.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("GET %s: unexpected status %d", e.URL, e.Code)
}

// NewClient returns an http.Client suitable for small upstream API calls.
func NewClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Second}
}

// JSON GETs rawURL and decodes at most 1 MiB of JSON into dst.
func JSON(ctx context.Context, client *http.Client, rawURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a little so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return &StatusError{URL: req.URL.Redacted(), Code: resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", req.URL.Redacted(), err)
	}
	return nil
}
