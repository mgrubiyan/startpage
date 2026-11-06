package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const maxBodyBytes = 64 << 10

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; the best we can do is log.
		slog.Warn("write json response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// decodeJSON reads a single JSON object from the request body into dst,
// rejecting unknown fields, trailing data and oversized bodies.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return jsonError(err)
	}
	// More() would miss a stray '}' or ']', so require a clean EOF instead.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return jsonError(err)
		}
		return errors.New("invalid json: unexpected data after object")
	}
	return nil
}

var errBodyTooLarge = fmt.Errorf("body larger than %d bytes", maxBodyBytes)

func jsonError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return errBodyTooLarge
	}
	return fmt.Errorf("invalid json: %w", err)
}

// badBody reports a decodeJSON failure: 413 for oversized bodies, 400 otherwise.
func badBody(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errBodyTooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	writeError(w, status, err.Error())
}
