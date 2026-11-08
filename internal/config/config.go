// Package config reads service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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
	// Demo runs a public read-only showcase: no login, every write refused.
	Demo bool
	// Telegram alerts from the monitor; both empty means no alerts.
	TelegramToken  string
	TelegramChatID string
}

// telegramToken is the shape of a token from @BotFather: "<bot id>:<secret>".
// Checked so a paste error fails at startup, not at the first outage.
var telegramToken = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

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
	var err error
	if cfg.Demo, err = parseBool(getenv, "STARTPAGE_DEMO", false); err != nil {
		errs = append(errs, err)
	}
	switch {
	case cfg.Demo && cfg.Password != "":
		// A password means this was meant to be a real instance; flipping it to demo
		// would publish its bookmarks, settings and services. Refuse instead.
		errs = append(errs, errors.New("STARTPAGE_DEMO and STARTPAGE_PASSWORD are mutually exclusive: a demo has no login, so run it as a separate instance"))
	case cfg.Demo:
		// No login in demo mode, so no password to check.
	case len(cfg.Password) < minPasswordLen:
		errs = append(errs, fmt.Errorf("STARTPAGE_PASSWORD must be at least %d characters", minPasswordLen))
	case len(cfg.Password) > maxPasswordBytes:
		errs = append(errs, fmt.Errorf("STARTPAGE_PASSWORD must be at most %d bytes (bcrypt limit)", maxPasswordBytes))
	}

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
	cfg.TelegramToken, cfg.TelegramChatID = getenv("TELEGRAM_BOT_TOKEN"), getenv("TELEGRAM_CHAT_ID")
	switch {
	case cfg.TelegramToken == "" && cfg.TelegramChatID == "":
	case cfg.Demo:
		// The demo seeds a service that is always down; its alerts would be noise.
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID are for a real instance, not the demo"))
	case cfg.TelegramToken == "" || cfg.TelegramChatID == "":
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID must be set together"))
	case !telegramToken.MatchString(cfg.TelegramToken):
		// Never echo the value: it is a secret.
		errs = append(errs, errors.New("TELEGRAM_BOT_TOKEN doesn't look like a token from @BotFather (digits:secret)"))
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
