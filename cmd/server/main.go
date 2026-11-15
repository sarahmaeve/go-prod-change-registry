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
	"strings"
	"syscall"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/config"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	postgresdb "github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
	"github.com/sarahmaeve/go-prod-change-registry/internal/router"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	postgresstore "github.com/sarahmaeve/go-prod-change-registry/internal/store/postgres"
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
// fatal error. Returning an error lets deferred resource cleanup complete
// before main exits the process.
func run(cfg *config.Config) error {
	if cfg.AutoMigrate {
		if err := postgresdb.Migrate(cfg.DatabaseURL, cfg.DBConnectTimeout); err != nil {
			return fmt.Errorf("run migrations: %w", err)
		}
	}

	pool, err := postgresdb.Open(context.Background(), cfg.DatabaseURL, postgresdb.PoolOptions{
		MaxConnections: cfg.DBMaxConnections,
		ConnectTimeout: cfg.DBConnectTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	slog.Info(
		"PostgreSQL connection pool opened",
		"max_connections", cfg.DBMaxConnections,
		"slow_query_threshold", cfg.DBSlowQueryThreshold,
	)

	store := postgresstore.New(pool, cfg.DBSlowQueryThreshold)
	svc := service.NewChangeService(store)
	apiHandler := handler.NewAPIHandler(svc, pool)
	dashHandler := handler.NewDashboardHandler(svc, cfg.DashboardRefreshSec, cfg.SessionSecret)
	providerOpts := humanauth.ProviderOptions{
		ClientID:        cfg.OAuthClientID,
		ClientSecret:    cfg.OAuthClientSecret,
		RedirectURL:     strings.TrimRight(cfg.PublicURL, "/") + "/auth/callback",
		IssuerURL:       cfg.OIDCIssuerURL,
		AllowedOrgs:     cfg.AllowedOrgs,
		AllowedSubjects: cfg.HumanAuthAllowedSubjects,
		AllowAny:        cfg.HumanAuthAllowAny,
	}
	humanAuthOpts := handler.HumanAuthOptions{
		SessionSecret:   cfg.SessionSecret,
		CookieSecure:    cfg.CookieSecure,
		SessionDuration: cfg.HumanSessionDuration,
	}
	var humanAuthHandler *handler.HumanAuthHandler
	if cfg.HumanAuthProvider == "beyond" {
		humanAuthHandler = handler.NewBeyondHumanAuthHandler(humanauth.NewBeyond(providerOpts), humanAuthOpts)
	} else {
		authCtx, cancelAuth := context.WithTimeout(context.Background(), cfg.DBConnectTimeout)
		authenticator, err := humanauth.New(authCtx, cfg.HumanAuthProvider, providerOpts)
		cancelAuth()
		if err != nil {
			return fmt.Errorf("configure human authentication: %w", err)
		}
		humanAuthHandler = handler.NewHumanAuthHandler(authenticator, humanAuthOpts)
	}

	mcpHandler := handler.NewMCPHandler(svc, cfg.PublicURL)
	r := router.New(apiHandler, dashHandler, humanAuthHandler, mcpHandler, cfg)
	srv := &http.Server{
		Addr:         cfg.Addr,
		Handler:      r,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdownCh)

	slog.Info("starting server", "addr", cfg.Addr)
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	if err := serveUntilShutdown(srv, listener, shutdownCh, cfg.ShutdownTimeout); err != nil {
		return err
	}

	slog.Info("server stopped gracefully")
	return nil
}

// serveUntilShutdown owns the complete HTTP server lifecycle. In particular,
// it does not return after the listener closes until Shutdown has finished
// waiting for active handlers (or its configured deadline has expired).
func serveUntilShutdown(
	srv *http.Server,
	listener net.Listener,
	shutdownCh <-chan os.Signal,
	shutdownTimeout time.Duration,
) error {
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- srv.Serve(listener)
	}()

	select {
	case err := <-serveErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("server exited with error: %w", err)
		}
		return nil
	case sig := <-shutdownCh:
		slog.Info("received shutdown signal", "signal", sig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownErr := srv.Shutdown(ctx)
	serveErr := <-serveErrCh

	if shutdownErr != nil {
		return fmt.Errorf("server shutdown: %w", shutdownErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("server exited with error: %w", serveErr)
	}
	return nil
}
