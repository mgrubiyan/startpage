// Command startpage runs the personal start page server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
