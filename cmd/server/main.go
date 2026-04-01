package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
)

func main() {
	// Load configuration from environment.
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Open SQLite store.
	store, err := sqlitestore.New(cfg.DatabasePath, cfg.DBBusyTimeout, cfg.DBSlowQueryThreshold)
	if err != nil {
		slog.Error("failed to open database", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Run migrations if configured.
	if cfg.AutoMigrate {
		if err := runMigrations(store); err != nil {
			slog.Error("failed to run migrations", "error", err)
			os.Exit(1)
		}
	}

	// Create service and handlers.
	svc := service.NewChangeService(store)
	apiHandler := handler.NewAPIHandler(svc)
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
func runMigrations(store *sqlitestore.Store) error {
	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return err
	}

	dbDriver, err := sqlitedriver.WithInstance(store.GetDB(), &sqlitedriver.Config{})
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

	// If a previous migration left the DB in a dirty state (e.g., a migration
	// partially applied columns that already existed), resolve it before proceeding.
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
		// Handle SQLite "duplicate column name" errors gracefully.
		// This occurs when a migration adds a column that already exists,
		// e.g., from a schema change that was applied outside of migrations.
		if strings.Contains(err.Error(), "duplicate column name") {
			slog.Warn(
				"migration has duplicate column, forcing version",
				"error", err,
			)
			ver, _, _ := m.Version()
			if ferr := m.Force(int(ver)); ferr != nil {
				return ferr
			}
		} else {
			return err
		}
	}

	slog.Info("database migrations applied successfully")
	return nil
}
