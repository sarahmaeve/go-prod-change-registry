package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/config"
)

// validSessionSecret is a 32-byte test value that satisfies the minimum
// length enforced by loadSessionSecret. Tests that don't care about the
// secret's contents use this constant so they can't be silently broken by
// tightening that rule further.
const validSessionSecret = "0123456789abcdef0123456789abcdef"

func TestLoad(t *testing.T) {
	// NOT parallel — t.Setenv is incompatible with t.Parallel().
	t.Setenv("PCR_DATABASE_URL", "postgres://pcr@postgres.example/pcr?sslmode=require")
	t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "github")
	t.Setenv("PCR_PUBLIC_URL", "https://changes.example.com")
	t.Setenv("PCR_OAUTH_CLIENT_ID", "test-client-id")
	t.Setenv("PCR_OAUTH_CLIENT_SECRET", "test-client-secret")
	t.Setenv("PCR_OIDC_ISSUER_URL", "")
	t.Setenv("PCR_ALLOWED_ORGS", "")
	t.Setenv("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS", "")
	t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "true")
	t.Setenv("PCR_HUMAN_SESSION_DURATION", "")

	// clearOptionalEnv blanks all optional PCR_ env vars so tests
	// are not affected by host environment variables.
	clearOptionalEnv := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{
			"PCR_ADDR", "PCR_SESSION_SECRET",
			"PCR_COOKIE_SECURE", "PCR_REQUIRE_AUTH_READS", "PCR_AUTO_MIGRATE",
			"PCR_DASHBOARD_REFRESH_SEC", "PCR_READ_TIMEOUT",
			"PCR_WRITE_TIMEOUT", "PCR_SHUTDOWN_TIMEOUT",
			"PCR_DB_CONNECT_TIMEOUT", "PCR_DB_MAX_CONNECTIONS",
			"PCR_DB_SLOW_QUERY_THRESHOLD",
		} {
			t.Setenv(key, "")
		}
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "github")
		t.Setenv("PCR_PUBLIC_URL", "https://changes.example.com")
		t.Setenv("PCR_OAUTH_CLIENT_ID", "test-client-id")
		t.Setenv("PCR_OAUTH_CLIENT_SECRET", "test-client-secret")
		t.Setenv("PCR_OIDC_ISSUER_URL", "")
		t.Setenv("PCR_ALLOWED_ORGS", "")
		t.Setenv("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS", "")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "true")
		t.Setenv("PCR_HUMAN_SESSION_DURATION", "")
	}

	t.Run("minimum valid config uses all defaults", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_DATABASE_URL", "postgres://pcr@postgres.example/pcr?sslmode=require")
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
		if cfg.DatabaseURL != "postgres://pcr@postgres.example/pcr?sslmode=require" {
			t.Errorf("DatabaseURL = %q, want configured PostgreSQL URL", cfg.DatabaseURL)
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
		if cfg.DBConnectTimeout != 5*time.Second {
			t.Errorf("DBConnectTimeout = %v, want 5s", cfg.DBConnectTimeout)
		}
		if cfg.DBMaxConnections != 10 {
			t.Errorf("DBMaxConnections = %d, want 10", cfg.DBMaxConnections)
		}
		if cfg.DBSlowQueryThreshold != 100*time.Millisecond {
			t.Errorf("DBSlowQueryThreshold = %v, want 100ms", cfg.DBSlowQueryThreshold)
		}
		if cfg.HumanAuthProvider != "github" {
			t.Errorf("HumanAuthProvider = %q, want github", cfg.HumanAuthProvider)
		}
		if cfg.PublicURL != "https://changes.example.com" {
			t.Errorf("PublicURL = %q, want https://changes.example.com", cfg.PublicURL)
		}
		if cfg.HumanSessionDuration != 12*time.Hour {
			t.Errorf("HumanSessionDuration = %v, want 12h", cfg.HumanSessionDuration)
		}
		if !cfg.HumanAuthAllowAny {
			t.Error("HumanAuthAllowAny = false, want true")
		}
	})

	t.Run("google organization domains and subjects are normalized", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "google")
		t.Setenv("PCR_ALLOWED_ORGS", " Example.COM, subsidiary.example.com ")
		t.Setenv("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS", "google:123, google:456")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")
		t.Setenv("PCR_HUMAN_SESSION_DURATION", "8h")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if got, want := strings.Join(cfg.AllowedOrgs, ","), "example.com,subsidiary.example.com"; got != want {
			t.Errorf("AllowedOrgs = %q, want %q", got, want)
		}
		if got, want := strings.Join(cfg.HumanAuthAllowedSubjects, ","), "google:123,google:456"; got != want {
			t.Errorf("HumanAuthAllowedSubjects = %q, want %q", got, want)
		}
		if cfg.HumanSessionDuration != 8*time.Hour {
			t.Errorf("HumanSessionDuration = %v, want 8h", cfg.HumanSessionDuration)
		}
	})

	t.Run("google organization domains enforce DNS boundaries", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "google")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")

		maxLabel := strings.Repeat("a", 63)
		maxDomainPrefix := strings.Repeat(maxLabel+".", 3)
		tests := []struct {
			name   string
			domain string
			valid  bool
		}{
			{name: "maximum label", domain: maxLabel + ".com", valid: true},
			{name: "maximum domain", domain: maxDomainPrefix + strings.Repeat("b", 61), valid: true},
			{name: "digits and interior hyphens", domain: "team-1.example.com", valid: true},
			{name: "punycode", domain: "xn--bcher-kva.example", valid: true},
			{name: "oversized label", domain: maxLabel + "a.com"},
			{name: "oversized domain", domain: maxDomainPrefix + strings.Repeat("b", 62)},
			{name: "single label", domain: "localhost"},
			{name: "empty first label", domain: ".example.com"},
			{name: "empty middle label", domain: "team..example.com"},
			{name: "empty last label", domain: "example.com."},
			{name: "leading hyphen", domain: "-team.example.com"},
			{name: "trailing hyphen", domain: "team-.example.com"},
			{name: "underscore", domain: "team_one.example.com"},
			{name: "unicode label", domain: "bücher.example"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				t.Setenv("PCR_ALLOWED_ORGS", test.domain)
				_, err := config.Load()
				if test.valid {
					if err != nil {
						t.Fatalf("Load() error = %v, want valid Google domain", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "invalid Google Workspace domain") {
					t.Fatalf("Load() error = %v, want invalid Google domain error", err)
				}
			})
		}
	})

	t.Run("authentik preserves exact group names and validates issuer", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "authentik")
		t.Setenv("PCR_OIDC_ISSUER_URL", "https://auth.example.com/application/o/pcr/")
		t.Setenv("PCR_ALLOWED_ORGS", "Platform Operators,Production-Readers")
		t.Setenv("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS", "authentik:https://auth.example.com/application/o/pcr/:user-123")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if got, want := cfg.OIDCIssuerURL, "https://auth.example.com/application/o/pcr/"; got != want {
			t.Errorf("OIDCIssuerURL = %q, want %q", got, want)
		}
		if got, want := strings.Join(cfg.AllowedOrgs, ","), "Platform Operators,Production-Readers"; got != want {
			t.Errorf("AllowedOrgs = %q, want %q", got, want)
		}
	})

	t.Run("beyond uses exact groups without PCR OAuth credentials", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "beyond")
		t.Setenv("PCR_OAUTH_CLIENT_ID", "")
		t.Setenv("PCR_OAUTH_CLIENT_SECRET", "")
		t.Setenv("PCR_ALLOWED_ORGS", "Platform Operators,Production-Readers")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if cfg.HumanAuthProvider != "beyond" {
			t.Errorf("HumanAuthProvider = %q, want beyond", cfg.HumanAuthProvider)
		}
		if got, want := strings.Join(cfg.AllowedOrgs, ","), "Platform Operators,Production-Readers"; got != want {
			t.Errorf("AllowedOrgs = %q, want %q", got, want)
		}
	})

	t.Run("invalid human authentication configuration fails closed", func(t *testing.T) {
		tests := []struct {
			name  string
			key   string
			value string
		}{
			{name: "missing provider", key: "PCR_HUMAN_AUTH_PROVIDER", value: ""},
			{name: "unknown provider", key: "PCR_HUMAN_AUTH_PROVIDER", value: "gitlab"},
			{name: "missing public URL", key: "PCR_PUBLIC_URL", value: ""},
			{name: "public URL has query", key: "PCR_PUBLIC_URL", value: "https://changes.example.com?next=evil"},
			{name: "insecure non-loopback public URL", key: "PCR_PUBLIC_URL", value: "http://changes.example.com"},
			{name: "missing client ID", key: "PCR_OAUTH_CLIENT_ID", value: ""},
			{name: "missing client secret", key: "PCR_OAUTH_CLIENT_SECRET", value: ""},
			{name: "nonpositive session duration", key: "PCR_HUMAN_SESSION_DURATION", value: "0s"},
			{name: "unreasonably long session duration", key: "PCR_HUMAN_SESSION_DURATION", value: "168h1m"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				clearOptionalEnv(t)
				t.Setenv("PCR_API_TOKENS", "tok1")
				t.Setenv(tc.key, tc.value)

				_, err := config.Load()
				if err == nil {
					t.Fatalf("Load() error = nil, want %s error", tc.key)
				}
				if !strings.Contains(err.Error(), tc.key) {
					t.Errorf("Load() error = %q, want %s context", err, tc.key)
				}
			})
		}
	})

	t.Run("authentik issuer configuration fails closed", func(t *testing.T) {
		tests := []string{
			"",
			"https://auth.example.com/",
			"https://auth.example.com/application/o/pcr",
			"http://auth.example.com/application/o/pcr/",
			"https://auth.example.com/application/o/pcr/?query=bad",
		}
		for _, issuer := range tests {
			t.Run(issuer, func(t *testing.T) {
				clearOptionalEnv(t)
				t.Setenv("PCR_API_TOKENS", "tok1")
				t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "authentik")
				t.Setenv("PCR_OIDC_ISSUER_URL", issuer)

				_, err := config.Load()
				if err == nil || !strings.Contains(err.Error(), "PCR_OIDC_ISSUER_URL") {
					t.Errorf("Load() error = %v, want PCR_OIDC_ISSUER_URL error", err)
				}
			})
		}
	})

	t.Run("authorization policy is explicit", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")

		_, err := config.Load()
		if err == nil || !strings.Contains(err.Error(), "PCR_ALLOWED_ORGS") {
			t.Fatalf("Load() error = %v, want missing authorization policy error", err)
		}

		t.Setenv("PCR_ALLOWED_ORGS", "example")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "true")
		_, err = config.Load()
		if err == nil || !strings.Contains(err.Error(), "PCR_HUMAN_AUTH_ALLOW_ANY") {
			t.Fatalf("Load() error = %v, want contradictory policy error", err)
		}
	})

	t.Run("full config with every env var set", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "alpha, bravo, charlie")
		t.Setenv("PCR_ADDR", ":9090")
		t.Setenv("PCR_DATABASE_URL", "postgres://pcr@db.internal/pcr?sslmode=verify-full")
		t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
		t.Setenv("PCR_REQUIRE_AUTH_READS", "false")
		t.Setenv("PCR_AUTO_MIGRATE", "0")
		t.Setenv("PCR_DASHBOARD_REFRESH_SEC", "30")
		t.Setenv("PCR_READ_TIMEOUT", "2s")
		t.Setenv("PCR_WRITE_TIMEOUT", "20s")
		t.Setenv("PCR_SHUTDOWN_TIMEOUT", "30s")
		t.Setenv("PCR_DB_CONNECT_TIMEOUT", "10s")
		t.Setenv("PCR_DB_MAX_CONNECTIONS", "25")
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
		if cfg.DatabaseURL != "postgres://pcr@db.internal/pcr?sslmode=verify-full" {
			t.Errorf("DatabaseURL = %q, want configured PostgreSQL URL", cfg.DatabaseURL)
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
		if cfg.DBConnectTimeout != 10*time.Second {
			t.Errorf("DBConnectTimeout = %v, want 10s", cfg.DBConnectTimeout)
		}
		if cfg.DBMaxConnections != 25 {
			t.Errorf("DBMaxConnections = %d, want 25", cfg.DBMaxConnections)
		}
		if cfg.DBSlowQueryThreshold != 250*time.Millisecond {
			t.Errorf("DBSlowQueryThreshold = %v, want 250ms", cfg.DBSlowQueryThreshold)
		}
	})

	t.Run("missing PCR_API_TOKENS returns error", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "")
		t.Setenv("PCR_DATABASE_URL", "postgres://pcr@localhost/pcr")
		clearOptionalEnv(t)

		_, err := config.Load()
		if err == nil {
			t.Fatal("expected error when PCR_API_TOKENS is not set")
		}
	})

	t.Run("Beyond allows API identity without legacy tokens", func(t *testing.T) {
		clearOptionalEnv(t)
		t.Setenv("PCR_API_TOKENS", "")
		t.Setenv("PCR_HUMAN_AUTH_PROVIDER", "beyond")
		t.Setenv("PCR_OAUTH_CLIENT_ID", "")
		t.Setenv("PCR_OAUTH_CLIENT_SECRET", "")
		t.Setenv("PCR_ALLOWED_ORGS", "example-readers")
		t.Setenv("PCR_HUMAN_AUTH_ALLOW_ANY", "false")

		cfg, err := config.Load()
		if err != nil {
			t.Fatalf("Load(): %v", err)
		}
		if len(cfg.APITokens) != 0 {
			t.Fatalf("APITokens = %v, want none", cfg.APITokens)
		}
	})

	t.Run("missing PCR_DATABASE_URL returns error", func(t *testing.T) {
		t.Setenv("PCR_API_TOKENS", "tok1")
		t.Setenv("PCR_DATABASE_URL", "")
		clearOptionalEnv(t)

		_, err := config.Load()
		if err == nil {
			t.Fatal("Load() error = nil, want missing PCR_DATABASE_URL error")
		}
		if !strings.Contains(err.Error(), "PCR_DATABASE_URL") {
			t.Errorf("Load() error = %q, want PCR_DATABASE_URL context", err)
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

	t.Run("invalid operational values return actionable errors", func(t *testing.T) {
		tests := []struct {
			key   string
			value string
		}{
			{key: "PCR_DASHBOARD_REFRESH_SEC", value: "-1"},
			{key: "PCR_READ_TIMEOUT", value: "0s"},
			{key: "PCR_READ_TIMEOUT", value: "-1s"},
			{key: "PCR_WRITE_TIMEOUT", value: "0s"},
			{key: "PCR_SHUTDOWN_TIMEOUT", value: "-1s"},
			{key: "PCR_DB_CONNECT_TIMEOUT", value: "0s"},
			{key: "PCR_DB_MAX_CONNECTIONS", value: "0"},
			{key: "PCR_DB_MAX_CONNECTIONS", value: "-1"},
			{key: "PCR_DB_MAX_CONNECTIONS", value: "2147483648"},
			{key: "PCR_DB_SLOW_QUERY_THRESHOLD", value: "-1ms"},
		}

		for _, tc := range tests {
			t.Run(tc.key+"="+tc.value, func(t *testing.T) {
				clearOptionalEnv(t)
				t.Setenv("PCR_API_TOKENS", "tok1")
				t.Setenv("PCR_SESSION_SECRET", validSessionSecret)
				t.Setenv(tc.key, tc.value)

				_, err := config.Load()
				if err == nil {
					t.Fatalf("Load() error = nil, want validation error for %s", tc.key)
				}
				if !strings.Contains(err.Error(), tc.key) {
					t.Fatalf("Load() error = %q, want variable %s", err, tc.key)
				}
			})
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
