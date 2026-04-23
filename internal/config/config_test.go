package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/config"
)

// validSessionSecret is a 32-byte test value that satisfies the minimum
// length enforced by loadSessionSecret. Tests that don't care about the
// secret's contents use this constant so they can't be silently broken by
// tightening that rule further.
const validSessionSecret = "0123456789abcdef0123456789abcdef"

func TestLoad(t *testing.T) {
	// NOT parallel — t.Setenv is incompatible with t.Parallel().

	// clearOptionalEnv blanks all optional PCR_ env vars so tests
	// are not affected by host environment variables.
	clearOptionalEnv := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{
			"PCR_ADDR", "PCR_DATABASE_PATH", "PCR_SESSION_SECRET",
			"PCR_COOKIE_SECURE", "PCR_REQUIRE_AUTH_READS", "PCR_AUTO_MIGRATE",
			"PCR_DASHBOARD_REFRESH_SEC", "PCR_READ_TIMEOUT",
			"PCR_WRITE_TIMEOUT", "PCR_SHUTDOWN_TIMEOUT",
			"PCR_DB_BUSY_TIMEOUT", "PCR_DB_SLOW_QUERY_THRESHOLD",
		} {
			t.Setenv(key, "")
		}
	}

	t.Run("minimum valid config uses all defaults", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "tok1")
		clearOptionalEnv(t)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(cfg.APITokens) != 1 || cfg.APITokens[0] != "tok1" {
			t.Fatalf("APITokens = %v, want [tok1]", cfg.APITokens)
		}
		if len(cfg.SessionSecret) != 32 {
			t.Fatalf("SessionSecret length = %d, want 32", len(cfg.SessionSecret))
		}
		if cfg.Addr != ":8080" {
			t.Errorf("Addr = %q, want %q", cfg.Addr, ":8080")
		}
		if cfg.DatabasePath != "registry.db" {
			t.Errorf("DatabasePath = %q, want %q", cfg.DatabasePath, "registry.db")
		}
		if cfg.CookieSecure != true {
			t.Errorf("CookieSecure = %v, want true", cfg.CookieSecure)
		}
		if cfg.RequireAuthReads != true {
			t.Errorf("RequireAuthReads = %v, want true", cfg.RequireAuthReads)
		}
		if cfg.AutoMigrate != true {
			t.Errorf("AutoMigrate = %v, want true", cfg.AutoMigrate)
		}
		if cfg.DashboardRefreshSec != 60 {
			t.Errorf("DashboardRefreshSec = %d, want 60", cfg.DashboardRefreshSec)
		}
		if cfg.ReadTimeout != 5*time.Second {
			t.Errorf("ReadTimeout = %v, want 5s", cfg.ReadTimeout)
		}
		if cfg.WriteTimeout != 10*time.Second {
			t.Errorf("WriteTimeout = %v, want 10s", cfg.WriteTimeout)
		}
		if cfg.ShutdownTimeout != 15*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
		}
		if cfg.DBBusyTimeout != 5*time.Second {
			t.Errorf("DBBusyTimeout = %v, want 5s", cfg.DBBusyTimeout)
		}
		if cfg.DBSlowQueryThreshold != 100*time.Millisecond {
			t.Errorf("DBSlowQueryThreshold = %v, want 100ms", cfg.DBSlowQueryThreshold)
		}
	})

	t.Run("full config with every env var set", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "alpha, bravo, charlie")
		t.Setenv("PCR_ADDR", ":9090")
		t.Setenv("PCR_DATABASE_PATH", "/tmp/test.db")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_REQUIRE_AUTH_READS", "false")
		t.Setenv("PCR_AUTO_MIGRATE", "0")
		t.Setenv("PCR_DASHBOARD_REFRESH_SEC", "30")
		t.Setenv("PCR_READ_TIMEOUT", "2s")
		t.Setenv("PCR_WRITE_TIMEOUT", "20s")
		t.Setenv("PCR_SHUTDOWN_TIMEOUT", "30s")
		t.Setenv("PCR_DB_BUSY_TIMEOUT", "10s")
		t.Setenv("PCR_DB_SLOW_QUERY_THRESHOLD", "250ms")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		wantTokens := []string{"alpha", "bravo", "charlie"}
		if len(cfg.APITokens) != len(wantTokens) {
			t.Fatalf("APITokens len = %d, want %d", len(cfg.APITokens), len(wantTokens))
		}
		for i, want := range wantTokens {
			if cfg.APITokens[i] != want {
				t.Errorf("APITokens[%d] = %q, want %q", i, cfg.APITokens[i], want)
			}
		}
		if string(cfg.SessionSecret) != validSessionSecret {
			t.Errorf("SessionSecret = %q, want %q", cfg.SessionSecret, validSessionSecret)
		}
		if cfg.Addr != ":9090" {
			t.Errorf("Addr = %q, want %q", cfg.Addr, ":9090")
		}
		if cfg.DatabasePath != "/tmp/test.db" {
			t.Errorf("DatabasePath = %q, want %q", cfg.DatabasePath, "/tmp/test.db")
		}
		if cfg.RequireAuthReads != false {
			t.Errorf("RequireAuthReads = %v, want false", cfg.RequireAuthReads)
		}
		if cfg.AutoMigrate != false {
			t.Errorf("AutoMigrate = %v, want false", cfg.AutoMigrate)
		}
		if cfg.DashboardRefreshSec != 30 {
			t.Errorf("DashboardRefreshSec = %d, want 30", cfg.DashboardRefreshSec)
		}
		if cfg.ReadTimeout != 2*time.Second {
			t.Errorf("ReadTimeout = %v, want 2s", cfg.ReadTimeout)
		}
		if cfg.WriteTimeout != 20*time.Second {
			t.Errorf("WriteTimeout = %v, want 20s", cfg.WriteTimeout)
		}
		if cfg.ShutdownTimeout != 30*time.Second {
			t.Errorf("ShutdownTimeout = %v, want 30s", cfg.ShutdownTimeout)
		}
		if cfg.DBBusyTimeout != 10*time.Second {
			t.Errorf("DBBusyTimeout = %v, want 10s", cfg.DBBusyTimeout)
		}
		if cfg.DBSlowQueryThreshold != 250*time.Millisecond {
			t.Errorf("DBSlowQueryThreshold = %v, want 250ms", cfg.DBSlowQueryThreshold)
		}
	})

	t.Run("missing PCR_API_TOKENS returns error", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "")
		clearOptionalEnv(t)

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error when PCR_API_TOKENS is not set")
		}
	})

	t.Run("PCR_API_TOKENS with only whitespace and commas returns error", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", " , , , ")
		clearOptionalEnv(t)

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error when PCR_API_TOKENS contains no valid tokens")
		}
	})

	t.Run("PCR_API_TOKENS trims whitespace from individual tokens", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "  tok1  ,  tok2  ")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := []string{"tok1", "tok2"}
		if len(cfg.APITokens) != 2 {
			t.Fatalf("APITokens len = %d, want 2", len(cfg.APITokens))
		}
		for i, w := range want {
			if cfg.APITokens[i] != w {
				t.Errorf("APITokens[%d] = %q, want %q", i, cfg.APITokens[i], w)
			}
		}
	})

	t.Run("invalid bool for PCR_REQUIRE_AUTH_READS returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_REQUIRE_AUTH_READS", "not-a-bool")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for invalid PCR_REQUIRE_AUTH_READS")
		}
		if !strings.Contains(err.Error(), "PCR_REQUIRE_AUTH_READS") {
			t.Fatalf("expected error about PCR_REQUIRE_AUTH_READS, got: %v", err)
		}
	})

	t.Run("invalid bool for PCR_AUTO_MIGRATE returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_AUTO_MIGRATE", "not-a-bool")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for invalid PCR_AUTO_MIGRATE")
		}
		if !strings.Contains(err.Error(), "PCR_AUTO_MIGRATE") {
			t.Fatalf("expected error about PCR_AUTO_MIGRATE, got: %v", err)
		}
	})

	t.Run("invalid bool for PCR_COOKIE_SECURE returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_COOKIE_SECURE", "not-a-bool")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for invalid PCR_COOKIE_SECURE")
		}
		if !strings.Contains(err.Error(), "PCR_COOKIE_SECURE") {
			t.Fatalf("expected error about PCR_COOKIE_SECURE, got: %v", err)
		}
	})

	t.Run("invalid int for PCR_DASHBOARD_REFRESH_SEC returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_DASHBOARD_REFRESH_SEC", "abc")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for invalid PCR_DASHBOARD_REFRESH_SEC")
		}
		if !strings.Contains(err.Error(), "PCR_DASHBOARD_REFRESH_SEC") {
			t.Fatalf("expected error about PCR_DASHBOARD_REFRESH_SEC, got: %v", err)
		}
	})

	t.Run("invalid duration for PCR_READ_TIMEOUT returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_READ_TIMEOUT", "not-a-duration")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for invalid PCR_READ_TIMEOUT")
		}
		if !strings.Contains(err.Error(), "PCR_READ_TIMEOUT") {
			t.Fatalf("expected error about PCR_READ_TIMEOUT, got: %v", err)
		}
	})

	t.Run("generated session secret is 32 bytes and differs between calls", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", "")

		cfg1, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cfg1.SessionSecret) != 32 {
			t.Fatalf("SessionSecret length = %d, want 32", len(cfg1.SessionSecret))
		}

		cfg2, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error on second call: %v", err)
		}
		if string(cfg1.SessionSecret) == string(cfg2.SessionSecret) {
			t.Error("two calls with no PCR_SESSION_SECRET produced identical secrets")
		}
	})

	t.Run("explicit session secret is used verbatim", func(t *testing.T) {
		// Use a distinct 32+ byte value (not the shared constant) so this
		// test proves the bytes are passed through unchanged rather than
		// overwritten with the default.
		const explicit = "explicit-session-secret-0123456789abcdef"
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", explicit)

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(cfg.SessionSecret) != explicit {
			t.Errorf("SessionSecret = %q, want %q", cfg.SessionSecret, explicit)
		}
	})

	t.Run("PCR_SESSION_SECRET shorter than 32 bytes returns error", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", "too-short")

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error for short PCR_SESSION_SECRET")
		}
		if !strings.Contains(err.Error(), "PCR_SESSION_SECRET") {
			t.Fatalf("expected error about PCR_SESSION_SECRET, got: %v", err)
		}
		if !strings.Contains(err.Error(), "32") {
			t.Errorf("expected error to mention the required minimum length, got: %v", err)
		}
	})

	t.Run("PCR_SESSION_SECRET exactly 32 bytes is accepted", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret) // exactly 32 bytes

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("unexpected error for boundary-length secret: %v", err)
		}
		if len(cfg.SessionSecret) != 32 {
			t.Errorf("SessionSecret length = %d, want 32", len(cfg.SessionSecret))
		}
	})
}
