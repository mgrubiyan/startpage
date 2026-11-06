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

	"github.com/mgrubiyan/startpage/internal/auth"
	"github.com/mgrubiyan/startpage/internal/cache"
	"github.com/mgrubiyan/startpage/internal/config"
	"github.com/mgrubiyan/startpage/internal/fetch"
	"github.com/mgrubiyan/startpage/internal/httpapi"
	"github.com/mgrubiyan/startpage/internal/monitor"
	"github.com/mgrubiyan/startpage/internal/rates"
	"github.com/mgrubiyan/startpage/internal/store/postgres"
	"github.com/mgrubiyan/startpage/internal/suggest"
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

	mon := monitor.New(db, logger, monitor.Config{Interval: cfg.MonitorInterval})
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

	authService, err := auth.New(cfg.Password, db, cfg.SessionTTL)
	if err != nil {
		return err
	}

	upstream := fetch.NewClient()
	weatherClient := weather.New(upstream)
	ratesClient := rates.New(upstream)
	suggestClient := suggest.New(upstream)

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
		Auth:              authService,
		LoginLimiter:      auth.NewLimiter(5, time.Minute),
		SecureCookie:      cfg.SecureCookie,
		TrustProxy:        cfg.TrustProxy,
		OnServicesChanged: mon.Trigger,
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
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

// healthcheck GETs /healthz on the local listener described by addr (STARTPAGE_ADDR).
func healthcheck(addr string) error {
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("bad STARTPAGE_ADDR %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/healthz", nil)
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
