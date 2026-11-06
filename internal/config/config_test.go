package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":       "postgres://localhost/startpage",
		"STARTPAGE_PASSWORD": "correct-horse",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.SecureCookie || cfg.TrustProxy {
		t.Errorf("SecureCookie/TrustProxy must default to false, got %v/%v", cfg.SecureCookie, cfg.TrustProxy)
	}
	if cfg.SessionTTL != 30*24*time.Hour {
		t.Errorf("SessionTTL = %v", cfg.SessionTTL)
	}
	if cfg.MonitorInterval != time.Minute {
		t.Errorf("MonitorInterval = %v", cfg.MonitorInterval)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":               "postgres://db/startpage",
		"STARTPAGE_PASSWORD":         "correct-horse",
		"STARTPAGE_ADDR":             "127.0.0.1:9000",
		"STARTPAGE_SECURE_COOKIE":    "true",
		"STARTPAGE_TRUST_PROXY":      "1",
		"STARTPAGE_SESSION_TTL":      "12h",
		"STARTPAGE_MONITOR_INTERVAL": "30s",
		"LOG_LEVEL":                  "debug",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9000" || !cfg.SecureCookie || !cfg.TrustProxy ||
		cfg.SessionTTL != 12*time.Hour || cfg.MonitorInterval != 30*time.Second || cfg.LogLevel != slog.LevelDebug {
		t.Errorf("overrides not applied: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://db", "STARTPAGE_PASSWORD": "correct-horse"}
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"missing db", "DATABASE_URL", "", "DATABASE_URL is required"},
		{"short password", "STARTPAGE_PASSWORD", "short", "at least 8 characters"},
		{"bad bool", "STARTPAGE_SECURE_COOKIE", "yes please", "STARTPAGE_SECURE_COOKIE"},
		{"bad duration", "STARTPAGE_SESSION_TTL", "forever", "STARTPAGE_SESSION_TTL"},
		{"interval too small", "STARTPAGE_MONITOR_INTERVAL", "1s", "at least 5s"},
		{"bad log level", "LOG_LEVEL", "loud", "LOG_LEVEL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			m[tt.key] = tt.value
			_, err := Load(env(m))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
