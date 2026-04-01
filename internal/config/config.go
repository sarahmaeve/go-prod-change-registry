package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the application configuration sourced from environment variables.
type Config struct {
	Addr                string
	DatabasePath        string
	APITokens           []string
	RequireAuthReads    bool
	AutoMigrate         bool
	DashboardRefreshSec int
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	ShutdownTimeout     time.Duration
	DBBusyTimeout       time.Duration
	DBSlowQueryThreshold time.Duration
}

// Load reads configuration from environment variables and returns a Config.
// It returns an error if required values are missing or malformed.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:                envOrDefault("PCR_ADDR", ":8080"),
		DatabasePath:        envOrDefault("PCR_DATABASE_PATH", "registry.db"),
		RequireAuthReads:    true,
		AutoMigrate:         true,
		DashboardRefreshSec: 60,
		ReadTimeout:          5 * time.Second,
		WriteTimeout:         10 * time.Second,
		ShutdownTimeout:      15 * time.Second,
		DBBusyTimeout:        5 * time.Second,
		DBSlowQueryThreshold: 100 * time.Millisecond,
	}

	// PCR_API_TOKENS — required, comma-separated.
	tokensRaw := os.Getenv("PCR_API_TOKENS")
	if tokensRaw == "" {
		return nil, fmt.Errorf("PCR_API_TOKENS is required but not set")
	}
	tokens := make([]string, 0)
	for _, t := range strings.Split(tokensRaw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	cfg.APITokens = tokens
	if len(cfg.APITokens) == 0 {
		return nil, fmt.Errorf("PCR_API_TOKENS contains no valid tokens")
	}

	// PCR_REQUIRE_AUTH_READS
	if v := os.Getenv("PCR_REQUIRE_AUTH_READS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_REQUIRE_AUTH_READS: %w", err)
		}
		cfg.RequireAuthReads = b
	}

	// PCR_AUTO_MIGRATE
	if v := os.Getenv("PCR_AUTO_MIGRATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_AUTO_MIGRATE: %w", err)
		}
		cfg.AutoMigrate = b
	}

	// PCR_DASHBOARD_REFRESH_SEC
	if v := os.Getenv("PCR_DASHBOARD_REFRESH_SEC"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_DASHBOARD_REFRESH_SEC: %w", err)
		}
		cfg.DashboardRefreshSec = n
	}

	// PCR_READ_TIMEOUT
	if v := os.Getenv("PCR_READ_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_READ_TIMEOUT: %w", err)
		}
		cfg.ReadTimeout = d
	}

	// PCR_WRITE_TIMEOUT
	if v := os.Getenv("PCR_WRITE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_WRITE_TIMEOUT: %w", err)
		}
		cfg.WriteTimeout = d
	}

	// PCR_SHUTDOWN_TIMEOUT
	if v := os.Getenv("PCR_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = d
	}

	// PCR_DB_BUSY_TIMEOUT — how long SQLite waits for a write lock (default 5s).
	if v := os.Getenv("PCR_DB_BUSY_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_DB_BUSY_TIMEOUT: %w", err)
		}
		cfg.DBBusyTimeout = d
	}

	// PCR_DB_SLOW_QUERY_THRESHOLD — log a warning when a store operation exceeds this (default 100ms).
	if v := os.Getenv("PCR_DB_SLOW_QUERY_THRESHOLD"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("PCR_DB_SLOW_QUERY_THRESHOLD: %w", err)
		}
		cfg.DBSlowQueryThreshold = d
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
