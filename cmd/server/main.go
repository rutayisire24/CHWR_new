// Command server runs the National Community Health Worker Registry.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"chwr/internal/config"
	"chwr/internal/db"
	apphttp "chwr/internal/http"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	migrateOnly := flag.Bool("migrate", false, "apply migrations and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: levelFor(cfg),
	})))

	// Signals cancel this context; the pool and the listener both hang off it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migrations run on every start: one binary, one schema, no separate step
	// to forget during a deploy.
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	version, err := db.Version(ctx, pool)
	if err != nil {
		return err
	}
	slog.Info("schema ready", "version", version)

	if *migrateOnly {
		return nil
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           apphttp.New(pool),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		slog.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

func levelFor(cfg config.Config) slog.Level {
	if cfg.Prod() {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}
