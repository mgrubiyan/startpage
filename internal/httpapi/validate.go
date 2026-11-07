package httpapi

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxURLLen = 2048

// cleanText trims s and checks it is non-empty, valid UTF-8, free of control
// characters and at most maxRunes characters long (matching Postgres char_length).
func cleanText(s string, maxRunes int) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", errors.New("must not be empty")
	case !utf8.ValidString(s):
		return "", errors.New("must be valid UTF-8")
	case utf8.RuneCountInString(s) > maxRunes:
		return "", fmt.Errorf("must be at most %d characters", maxRunes)
	case strings.IndexFunc(s, unicode.IsControl) >= 0:
		return "", errors.New("must not contain control characters")
	}
	return s, nil
}

// cleanURL accepts only absolute http(s) URLs. Anything else (javascript:, data:,
// relative paths) is rejected because the URL ends up in an <a href> or an outgoing request.
func cleanURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("must not be empty")
	}
	if len(raw) > maxURLLen {
		return "", fmt.Errorf("must be at most %d bytes", maxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("must be an absolute http or https URL")
	}
	// Normalising percent-encodes non-ASCII text (one Cyrillic letter becomes six
	// characters), so check the stored form too, as the database counts it.
	clean := u.String()
	if utf8.RuneCountInString(clean) > maxURLLen {
		return "", fmt.Errorf("must be at most %d characters once encoded", maxURLLen)
	}
	return clean, nil
}
