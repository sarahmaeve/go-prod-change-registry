package config

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the application configuration sourced from environment variables.
type Config struct {
	Addr                 string
	DatabasePath         string
	APITokens            []string
	SessionSecret        []byte
	CookieSecure         bool
	RequireAuthReads     bool
	AutoMigrate          bool
	DashboardRefreshSec  int
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
	ShutdownTimeout      time.Duration
	DBBusyTimeout        time.Duration
	DBSlowQueryThreshold time.Duration
}

// Load reads configuration from environment variables and returns a Config.
// It returns an error if required values are missing or malformed.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:                 envOrDefault("PCR_ADDR", ":8080"),
		DatabasePath:         envOrDefault("PCR_DATABASE_PATH", "registry.db"),
		CookieSecure:         true,
		RequireAuthReads:     true,
		AutoMigrate:          true,
		DashboardRefreshSec:  60,
		ReadTimeout:          5 * time.Second,
		WriteTimeout:         10 * time.Second,
		ShutdownTimeout:      15 * time.Second,
		DBBusyTimeout:        5 * time.Second,
		DBSlowQueryThreshold: 100 * time.Millisecond,
	}

	if err := loadAPITokens(cfg); err != nil {
		return nil, err
	}
	if err := loadSessionSecret(cfg); err != nil {
		return nil, err
	}

	for _, err := range []error{
		optionalEnv("PCR_REQUIRE_AUTH_READS", strconv.ParseBool, &cfg.RequireAuthReads),
		optionalEnv("PCR_AUTO_MIGRATE", strconv.ParseBool, &cfg.AutoMigrate),
		optionalEnv("PCR_COOKIE_SECURE", strconv.ParseBool, &cfg.CookieSecure),
		optionalEnv("PCR_DASHBOARD_REFRESH_SEC", strconv.Atoi, &cfg.DashboardRefreshSec),
		optionalEnv("PCR_READ_TIMEOUT", time.ParseDuration, &cfg.ReadTimeout),
		optionalEnv("PCR_WRITE_TIMEOUT", time.ParseDuration, &cfg.WriteTimeout),
		optionalEnv("PCR_SHUTDOWN_TIMEOUT", time.ParseDuration, &cfg.ShutdownTimeout),
		optionalEnv("PCR_DB_BUSY_TIMEOUT", time.ParseDuration, &cfg.DBBusyTimeout),
		optionalEnv("PCR_DB_SLOW_QUERY_THRESHOLD", time.ParseDuration, &cfg.DBSlowQueryThreshold),
	} {
		if err != nil {
			return nil, err
		}
	}

	return cfg, nil
}

// loadAPITokens reads PCR_API_TOKENS (required, comma-separated) and writes
// the trimmed, non-empty tokens onto cfg. Returns an error if the var is
// unset or contains no valid tokens.
func loadAPITokens(cfg *Config) error {
	raw := os.Getenv("PCR_API_TOKENS")
	if raw == "" {
		return fmt.Errorf("PCR_API_TOKENS is required but not set")
	}

	parts := strings.Split(raw, ",")
	tokens := make([]string, 0, len(parts))
	for _, t := range parts {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			tokens = append(tokens, trimmed)
		}
	}
	if len(tokens) == 0 {
		return fmt.Errorf("PCR_API_TOKENS contains no valid tokens")
	}

	cfg.APITokens = tokens
	return nil
}

// loadSessionSecret reads PCR_SESSION_SECRET or generates a random 32-byte
// secret when unset. Generated secrets are ephemeral — sessions will not
// survive restarts, which we log loudly so operators notice in production.
func loadSessionSecret(cfg *Config) error {
	if v := os.Getenv("PCR_SESSION_SECRET"); v != "" {
		cfg.SessionSecret = []byte(v)
		return nil
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate session secret: %w", err)
	}
	cfg.SessionSecret = secret
	slog.Warn("PCR_SESSION_SECRET not set, using ephemeral secret (sessions will not survive restarts)")
	return nil
}

// optionalEnv reads an env var and, when set, parses it with parse and writes
// the result to dest. Returns nil when the var is unset or empty. Parse errors
// are wrapped with the env var name so callers get actionable messages.
func optionalEnv[T any](key string, parse func(string) (T, error), dest *T) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parsed, err := parse(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dest = parsed
	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
