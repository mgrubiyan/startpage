// Command startpage runs the personal start page server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mgrubiyan/startpage/internal/art"
	"github.com/mgrubiyan/startpage/internal/auth"
	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/config"
	"github.com/mgrubiyan/startpage/internal/demo"
	"github.com/mgrubiyan/startpage/internal/fetch"
	"github.com/mgrubiyan/startpage/internal/httpapi"
	"github.com/mgrubiyan/startpage/internal/monitor"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/store/postgres"
	"github.com/mgrubiyan/startpage/internal/suggest"
	"github.com/mgrubiyan/startpage/internal/telegram"
	"github.com/mgrubiyan/startpage/internal/weather"
	"github.com/mgrubiyan/startpage/web"
)

func main() {
	// "startpage healthcheck" probes a running instance; the distroless image has
	// no curl, so Docker's HEALTHCHECK calls the binary itself.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(os.Getenv("STARTPAGE_ADDR")); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("startpage stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	openCtx, cancelOpen := context.WithTimeout(ctx, 30*time.Second)
	db, err := postgres.Open(openCtx, cfg.DatabaseURL)
	cancelOpen()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	// Seed before the monitor starts, so its first round already probes the demo services.
	if cfg.Demo {
		selfURL, err := localURL(cfg.Addr)
		if err != nil {
			return err
		}
		if err := demo.Seed(ctx, db, selfURL); err != nil {
			return err
		}
	}

	var alerts monitor.Notifier // stays a nil interface when Telegram isn't configured
	if cfg.TelegramToken != "" {
		alerts = telegram.New(&http.Client{Timeout: 10 * time.Second}, cfg.TelegramToken, cfg.TelegramChatID)
		logger.Info("monitor alerts go to Telegram")
	}
	mon := monitor.New(db, logger, monitor.Config{Interval: cfg.MonitorInterval, Notifier: alerts})
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		mon.Run(monitorCtx)
	}()
	// Deferred calls run in reverse order, so this stops the monitor and waits for
	// in-flight probes before db.Close — on signal and on any early error return alike.
	defer func() {
		stopMonitor()
		<-monitorDone
	}()

	// Demo mode has no login: leave Auth nil (the API never calls it then) and
	// throttle the public suggestions proxy.
	var authService httpapi.Authenticator
	var suggestLimiter httpapi.RateLimiter
	if cfg.Demo {
		suggestLimiter = auth.NewLimiter(60, time.Minute)
		logger.Info("demo mode: read-only, no login")
		if !cfg.TrustProxy {
			logger.Warn("demo mode without STARTPAGE_TRUST_PROXY: behind a reverse proxy all visitors share one suggestions rate limit")
		}
	} else {
		svc, err := auth.New(ctx, cfg.Password, db, cfg.SessionTTL)
		if err != nil {
			return err
		}
		authService = svc
	}

	upstream := fetch.NewClient()
	weatherClient := weather.New(upstream)
	ratesClient := rates.New(upstream)
	suggestClient := suggest.New(upstream)
	// Its own client: a 3–6 MB painting needs longer than the 10 s API timeout.
	// Per request; one whole load (up to 3 attempts) gets 2 min in art.Daily.
	artwork := art.NewDaily(art.New(&http.Client{Timeout: 30 * time.Second}).Pick)
	go func() {
		// Load ahead of visitors, at startup and soon after each UTC midnight,
		// so nobody waits for the download. Today is a no-op once loaded.
		tick := time.NewTicker(30 * time.Minute)
		defer tick.Stop()
		for {
			if _, err := artwork.Today(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("painting of the day not loaded", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()

	handler := httpapi.New(httpapi.Deps{
		Logger:  logger,
		Static:  web.Static(),
		Store:   db,
		Ping:    db.Ping,
		Weather: cache.New(10*time.Minute, weatherClient.Current, cache.WithMaxEntries(50)),
		Rates: cache.New(time.Hour, func(ctx context.Context, _ string) (rates.Rates, error) {
			return ratesClient.Latest(ctx)
		}),
		Suggest:           cache.New(time.Hour, suggestClient.Suggest, cache.WithLoadTimeout(3*time.Second)),
		Art:               artwork,
		Auth:              authService,
		LoginLimiter:      auth.NewLimiter(5, time.Minute),
		SecureCookie:      cfg.SecureCookie,
		TrustProxy:        cfg.TrustProxy,
		OnServicesChanged: mon.Trigger,
		Demo:              cfg.Demo,
		SuggestLimiter:    suggestLimiter,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		// Restore default signal handling: a second Ctrl-C kills a stuck drain.
		stop()
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

// localURL is the base URL of the local listener described by addr (STARTPAGE_ADDR).
func localURL(addr string) (string, error) {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("bad STARTPAGE_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// healthcheck GETs /healthz on the local listener described by addr (STARTPAGE_ADDR).
func healthcheck(addr string) error {
	base, err := localURL(addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
