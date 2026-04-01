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
	"github.com/sarah/go-prod-change-registry/internal/router"
	"github.com/sarah/go-prod-change-registry/internal/service"
	sqlitestore "github.com/sarah/go-prod-change-registry/internal/store/sqlite"
	"github.com/sarah/go-prod-change-registry/migrations"

	_ "modernc.org/sqlite"
)

func main() {
	// Load configuration from environment.
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Open the database connection directly.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(%d)",
		cfg.DatabasePath,
		cfg.DBBusyTimeout.Milliseconds(),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	slog.Info(
		"sqlite database opened",
		"path", cfg.DatabasePath,
		"busy_timeout_ms", cfg.DBBusyTimeout.Milliseconds(),
		"slow_query_threshold", cfg.DBSlowQueryThreshold,
	)

	// Run migrations if configured.
	if cfg.AutoMigrate {
		if err := runMigrations(db); err != nil {
			slog.Error("failed to run migrations", "error", err)
			os.Exit(1)
		}
	}

	// Create SQLite store wrapping the open connection.
	store := sqlitestore.New(db, cfg.DBSlowQueryThreshold)

	// Create service and handlers.
	svc := service.NewChangeService(store)
	apiHandler := handler.NewAPIHandler(svc, db)
	dashHandler := handler.NewDashboardHandler(svc, cfg.DashboardRefreshSec)

	// Create router and HTTP server.
	r := router.New(apiHandler, dashHandler, cfg)
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	// Graceful shutdown handling.
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
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
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
