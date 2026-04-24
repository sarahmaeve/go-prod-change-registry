package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/golang-migrate/migrate/v4"
	sqlitedriver "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/sarah/go-prod-change-registry/internal/config"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/internal/router"
	"github.com/sarah/go-prod-change-registry/internal/service"
	sqlitestore "github.com/sarah/go-prod-change-registry/internal/store/sqlite"
	"github.com/sarah/go-prod-change-registry/migrations"

	_ "modernc.org/sqlite"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// run owns all deferred cleanup (e.g. db.Close), so errors must surface
	// here before os.Exit -- otherwise os.Exit would skip the defers.
	if err := run(cfg); err != nil {
		slog.Error("server fatal error", "error", err)
		os.Exit(1)
	}
}

// run wires dependencies, starts the server, and blocks until shutdown or
// fatal error. Returning an error (instead of calling os.Exit) lets deferred
// resource cleanup -- most importantly db.Close -- complete before exit.
func run(cfg *config.Config) error {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(%d)",
		cfg.DatabasePath,
		cfg.DBBusyTimeout.Milliseconds(),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			slog.Error("database close error", "error", cerr)
		}
	}()

	slog.Info(
		"sqlite database opened",
		"path", cfg.DatabasePath,
		"busy_timeout_ms", cfg.DBBusyTimeout.Milliseconds(),
		"slow_query_threshold", cfg.DBSlowQueryThreshold,
	)

	if cfg.AutoMigrate {
		if err := runMigrations(db); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	store := sqlitestore.New(db, cfg.DBSlowQueryThreshold)
	svc := service.NewChangeService(store)
	apiHandler := handler.NewAPIHandler(svc, db)
	dashHandler := handler.NewDashboardHandler(svc, cfg.DashboardRefreshSec, cfg.SessionSecret)
	loginHandler := handler.NewLoginHandler(cfg.APITokens, middleware.SessionOptions{
		Secret: cfg.SessionSecret,
		Secure: cfg.CookieSecure,
	})

	r := router.New(apiHandler, dashHandler, loginHandler, cfg)
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-shutdownCh
		slog.Info("received shutdown signal", "signal", sig)

		ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			slog.Error("server shutdown error", "error", err)
		}
	}()

	slog.Info("starting server", "addr", cfg.Addr)

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server exited with error: %w", err)
	}

	slog.Info("server stopped gracefully")
	return nil
}

// runMigrations applies database migrations from the embedded filesystem.
func runMigrations(db *sql.DB) error {
	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	dbDriver, err := sqlitedriver.WithInstance(db, &sqlitedriver.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithInstance(
		"iofs",
		sourceDriver,
		"sqlite",
		dbDriver,
	)
	if err != nil {
		return err
	}

	// Handle dirty state from a previously failed migration.
	version, dirty, verr := m.Version()
	if verr != nil && !errors.Is(verr, migrate.ErrNoChange) && !errors.Is(verr, migrate.ErrNilVersion) {
		return verr
	}
	if dirty {
		slog.Warn(
			"database is in dirty state, forcing version to resolve",
			"version", version,
		)
		if ferr := m.Force(int(version)); ferr != nil {
			return ferr
		}
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	slog.Info("database migrations applied successfully")
	return nil
}
