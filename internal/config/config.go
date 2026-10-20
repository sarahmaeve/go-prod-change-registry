package config

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Config holds the application configuration sourced from environment variables.
type Config struct {
	Addr                     string
	DatabaseURL              string
	APITokens                []string
	SessionSecret            []byte
	CookieSecure             bool
	RequireAuthReads         bool
	AutoMigrate              bool
	DashboardRefreshSec      int
	ReadTimeout              time.Duration
	WriteTimeout             time.Duration
	ShutdownTimeout          time.Duration
	DBConnectTimeout         time.Duration
	DBMaxConnections         int
	DBSlowQueryThreshold     time.Duration
	HumanAuthProvider        string
	PublicURL                string
	OAuthClientID            string
	OAuthClientSecret        string
	OIDCIssuerURL            string
	AllowedOrgs              []string
	HumanAuthAllowedSubjects []string
	HumanAuthAllowAny        bool
	HumanSessionDuration     time.Duration
}

// Load reads configuration from environment variables and returns a Config.
// It returns an error if required values are missing or malformed.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:                     envOrDefault("PCR_ADDR", ":8080"),
		DatabaseURL:              os.Getenv("PCR_DATABASE_URL"),
		CookieSecure:             true,
		RequireAuthReads:         true,
		AutoMigrate:              true,
		DashboardRefreshSec:      60,
		ReadTimeout:              5 * time.Second,
		WriteTimeout:             10 * time.Second,
		ShutdownTimeout:          15 * time.Second,
		DBConnectTimeout:         5 * time.Second,
		DBMaxConnections:         10,
		DBSlowQueryThreshold:     100 * time.Millisecond,
		HumanAuthProvider:        strings.ToLower(strings.TrimSpace(os.Getenv("PCR_HUMAN_AUTH_PROVIDER"))),
		PublicURL:                strings.TrimSpace(os.Getenv("PCR_PUBLIC_URL")),
		OAuthClientID:            strings.TrimSpace(os.Getenv("PCR_OAUTH_CLIENT_ID")),
		OAuthClientSecret:        os.Getenv("PCR_OAUTH_CLIENT_SECRET"),
		OIDCIssuerURL:            strings.TrimSpace(os.Getenv("PCR_OIDC_ISSUER_URL")),
		AllowedOrgs:              splitList(os.Getenv("PCR_ALLOWED_ORGS")),
		HumanAuthAllowedSubjects: splitList(os.Getenv("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS")),
		HumanSessionDuration:     12 * time.Hour,
	}

	if err := loadAPITokens(cfg); err != nil {
		return nil, err
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("PCR_DATABASE_URL is required but not set")
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
		optionalEnv("PCR_DB_CONNECT_TIMEOUT", time.ParseDuration, &cfg.DBConnectTimeout),
		optionalEnv("PCR_DB_MAX_CONNECTIONS", strconv.Atoi, &cfg.DBMaxConnections),
		optionalEnv("PCR_DB_SLOW_QUERY_THRESHOLD", time.ParseDuration, &cfg.DBSlowQueryThreshold),
		optionalEnv("PCR_HUMAN_AUTH_ALLOW_ANY", strconv.ParseBool, &cfg.HumanAuthAllowAny),
		optionalEnv("PCR_HUMAN_SESSION_DURATION", time.ParseDuration, &cfg.HumanSessionDuration),
	} {
		if err != nil {
			return nil, err
		}
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validate(cfg *Config) error {
	if cfg.DashboardRefreshSec < 0 {
		return fmt.Errorf("PCR_DASHBOARD_REFRESH_SEC must be greater than or equal to 0")
	}
	for _, timeout := range []struct {
		key   string
		value time.Duration
	}{
		{key: "PCR_READ_TIMEOUT", value: cfg.ReadTimeout},
		{key: "PCR_WRITE_TIMEOUT", value: cfg.WriteTimeout},
		{key: "PCR_SHUTDOWN_TIMEOUT", value: cfg.ShutdownTimeout},
		{key: "PCR_DB_CONNECT_TIMEOUT", value: cfg.DBConnectTimeout},
		{key: "PCR_DB_SLOW_QUERY_THRESHOLD", value: cfg.DBSlowQueryThreshold},
	} {
		if timeout.value <= 0 {
			return fmt.Errorf("%s must be greater than 0", timeout.key)
		}
	}
	if cfg.DBMaxConnections <= 0 || cfg.DBMaxConnections > math.MaxInt32 {
		return fmt.Errorf("PCR_DB_MAX_CONNECTIONS must be between 1 and %d", math.MaxInt32)
	}
	if err := validateHumanAuth(cfg); err != nil {
		return err
	}
	return nil
}

const maxHumanSessionDuration = 7 * 24 * time.Hour

var githubOrgPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,37}[a-z0-9])?$`)

func validateHumanAuth(cfg *Config) error {
	if err := validateHumanAuthProvider(cfg.HumanAuthProvider); err != nil {
		return err
	}
	if err := validatePublicURL(cfg.PublicURL); err != nil {
		return err
	}
	if cfg.HumanAuthProvider != "beyond" {
		if cfg.OAuthClientID == "" {
			return fmt.Errorf("PCR_OAUTH_CLIENT_ID is required but not set")
		}
		if cfg.OAuthClientSecret == "" {
			return fmt.Errorf("PCR_OAUTH_CLIENT_SECRET is required but not set")
		}
	}
	if cfg.HumanAuthProvider == "authentik" {
		if err := validateAuthentikIssuerURL(cfg.OIDCIssuerURL); err != nil {
			return err
		}
	}
	if cfg.HumanSessionDuration <= 0 || cfg.HumanSessionDuration > maxHumanSessionDuration {
		return fmt.Errorf("PCR_HUMAN_SESSION_DURATION must be greater than 0 and no more than %s", maxHumanSessionDuration)
	}

	if err := normalizeAllowedOrgs(cfg); err != nil {
		return err
	}
	if err := validateAllowedSubjects(cfg); err != nil {
		return err
	}
	return validateHumanAuthPolicy(cfg)
}

func validateHumanAuthProvider(provider string) error {
	switch provider {
	case "github", "google", "authentik", "beyond":
		return nil
	case "":
		return fmt.Errorf("PCR_HUMAN_AUTH_PROVIDER is required but not set")
	default:
		return fmt.Errorf("PCR_HUMAN_AUTH_PROVIDER must be github, google, authentik, or beyond")
	}
}

func normalizeAllowedOrgs(cfg *Config) error {
	for i, org := range cfg.AllowedOrgs {
		normalized, err := normalizeOrg(cfg.HumanAuthProvider, org)
		if err != nil {
			return err
		}
		cfg.AllowedOrgs[i] = normalized
	}
	return nil
}

func validateAllowedSubjects(cfg *Config) error {
	prefix := cfg.HumanAuthProvider + ":"
	if cfg.HumanAuthProvider == "authentik" {
		prefix += cfg.OIDCIssuerURL + ":"
	}
	for _, subject := range cfg.HumanAuthAllowedSubjects {
		if !strings.HasPrefix(subject, prefix) || strings.TrimPrefix(subject, prefix) == "" {
			return fmt.Errorf("PCR_HUMAN_AUTH_ALLOWED_SUBJECTS entry %q must start with %q and include a subject", subject, prefix)
		}
	}
	return nil
}

func validateHumanAuthPolicy(cfg *Config) error {
	hasRestrictions := len(cfg.AllowedOrgs) > 0 || len(cfg.HumanAuthAllowedSubjects) > 0
	if cfg.HumanAuthAllowAny && hasRestrictions {
		return fmt.Errorf("PCR_HUMAN_AUTH_ALLOW_ANY cannot be true when PCR_ALLOWED_ORGS or PCR_HUMAN_AUTH_ALLOWED_SUBJECTS is set")
	}
	if !cfg.HumanAuthAllowAny && !hasRestrictions {
		return fmt.Errorf("PCR_ALLOWED_ORGS or PCR_HUMAN_AUTH_ALLOWED_SUBJECTS is required unless PCR_HUMAN_AUTH_ALLOW_ANY=true")
	}
	return nil
}

func validatePublicURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("PCR_PUBLIC_URL is required but not set")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("PCR_PUBLIC_URL must be an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("PCR_PUBLIC_URL must be an origin without user info, path, query, or fragment")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("PCR_PUBLIC_URL must use https except for loopback development")
}

func normalizeOrg(provider, org string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(org))
	switch provider {
	case "github":
		if !githubOrgPattern.MatchString(normalized) {
			return "", fmt.Errorf("PCR_ALLOWED_ORGS contains invalid GitHub organization %q", org)
		}
	case "google":
		if !validDomain(normalized) {
			return "", fmt.Errorf("PCR_ALLOWED_ORGS contains invalid Google Workspace domain %q", org)
		}
	case "authentik", "beyond":
		normalized = strings.TrimSpace(org)
		if normalized == "" || strings.IndexFunc(normalized, unicode.IsControl) >= 0 {
			return "", fmt.Errorf("PCR_ALLOWED_ORGS contains invalid group %q", org)
		}
	}
	return normalized, nil
}

func validateAuthentikIssuerURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("PCR_OIDC_ISSUER_URL is required when PCR_HUMAN_AUTH_PROVIDER=authentik")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("PCR_OIDC_ISSUER_URL must be an absolute URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("PCR_OIDC_ISSUER_URL must not contain user info, a query, or a fragment")
	}
	applicationSlug := strings.Trim(strings.TrimPrefix(parsed.Path, "/application/o/"), "/")
	if !strings.HasPrefix(parsed.Path, "/application/o/") || applicationSlug == "" || strings.Contains(applicationSlug, "/") || !strings.HasSuffix(parsed.Path, "/") {
		return fmt.Errorf("PCR_OIDC_ISSUER_URL must use Authentik's per-provider /application/o/<slug>/ issuer")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" {
		host := parsed.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return fmt.Errorf("PCR_OIDC_ISSUER_URL must use https except for loopback development")
}

func validDomain(domain string) bool {
	if len(domain) == 0 || len(domain) > 253 || strings.Contains(domain, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !validDomainLabel(label) {
			return false
		}
	}
	return true
}

func validDomainLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func splitList(raw string) []string {
	var values []string
	for part := range strings.SplitSeq(raw, ",") {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// loadAPITokens reads PCR_API_TOKENS (comma-separated) and writes the trimmed,
// non-empty tokens onto cfg. Beyond deployments may omit legacy application
// tokens because the trusted edge authenticates API clients and injects their
// verified identity. Other providers still require at least one API token.
func loadAPITokens(cfg *Config) error {
	raw := os.Getenv("PCR_API_TOKENS")
	if raw == "" {
		if cfg.HumanAuthProvider == "beyond" {
			return nil
		}
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
		if cfg.HumanAuthProvider == "beyond" {
			return nil
		}
		return fmt.Errorf("PCR_API_TOKENS contains no valid tokens")
	}

	cfg.APITokens = tokens
	return nil
}

// minSessionSecretBytes is the minimum length accepted for PCR_SESSION_SECRET.
// The server HMAC-signs session cookies and CSRF tokens with this key, so a
// short secret weakens both. 32 bytes matches the length of the auto-generated
// fallback and gives SHA-256 HMAC a full-strength key.
const minSessionSecretBytes = 32

// loadSessionSecret reads PCR_SESSION_SECRET or generates a random 32-byte
// secret when unset. Generated secrets are ephemeral — sessions will not
// survive restarts, which we log loudly so operators notice in production.
// Explicit secrets shorter than minSessionSecretBytes are rejected to keep
// production HMAC signatures at full strength.
func loadSessionSecret(cfg *Config) error {
	if v := os.Getenv("PCR_SESSION_SECRET"); v != "" {
		if len(v) < minSessionSecretBytes {
			return fmt.Errorf(
				"PCR_SESSION_SECRET must be at least %d bytes; got %d",
				minSessionSecretBytes, len(v),
			)
		}
		cfg.SessionSecret = []byte(v)
		return nil
	}

	secret := make([]byte, minSessionSecretBytes)
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
