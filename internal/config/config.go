// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// Config is the full runtime configuration of the service.
type Config struct {
	Addr            string
	DatabaseURL     string
	Password        string
	SecureCookie    bool
	TrustProxy      bool
	SessionTTL      time.Duration
	MonitorInterval time.Duration
	LogLevel        slog.Level
}

const (
	minPasswordLen   = 8
	maxPasswordBytes = 72 // bcrypt rejects longer inputs
)

// Load builds a Config from getenv (os.Getenv in production, a map lookup in tests).
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:        withDefault(getenv("STARTPAGE_ADDR"), ":8080"),
		DatabaseURL: getenv("DATABASE_URL"),
		Password:    getenv("STARTPAGE_PASSWORD"),
	}

	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	switch {
	case len(cfg.Password) < minPasswordLen:
		errs = append(errs, fmt.Errorf("STARTPAGE_PASSWORD must be at least %d characters", minPasswordLen))
	case len(cfg.Password) > maxPasswordBytes:
		errs = append(errs, fmt.Errorf("STARTPAGE_PASSWORD must be at most %d bytes (bcrypt limit)", maxPasswordBytes))
	}

	var err error
	if cfg.SecureCookie, err = parseBool(getenv, "STARTPAGE_SECURE_COOKIE", false); err != nil {
		errs = append(errs, err)
	}
	if cfg.TrustProxy, err = parseBool(getenv, "STARTPAGE_TRUST_PROXY", false); err != nil {
		errs = append(errs, err)
	}
	if cfg.SessionTTL, err = parseDuration(getenv, "STARTPAGE_SESSION_TTL", 30*24*time.Hour, time.Minute); err != nil {
		errs = append(errs, err)
	}
	if cfg.MonitorInterval, err = parseDuration(getenv, "STARTPAGE_MONITOR_INTERVAL", time.Minute, 5*time.Second); err != nil {
		errs = append(errs, err)
	}
	if err = cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func parseBool(getenv func(string) string, key string, def bool) (bool, error) {
	raw := getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func parseDuration(getenv func(string) string, key string, def, minimum time.Duration) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if v < minimum {
		return 0, fmt.Errorf("%s: must be at least %s", key, minimum)
	}
	return v, nil
}
