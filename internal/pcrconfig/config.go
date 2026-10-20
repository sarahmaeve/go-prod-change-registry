// Package pcrconfig loads and resolves configuration for the pcr command.
package pcrconfig

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

const maxFileSize = 1 << 20

// ErrInvalid identifies invalid or unsafe user configuration.
var ErrInvalid = errors.New("invalid PCR configuration")

// File is the versioned on-disk configuration schema.
type File struct {
	Version    int    `toml:"version"`
	URL        string `toml:"url"`
	Credential string `toml:"credential,omitempty"`
}

// Values are the effective settings after applying flag, environment, and file
// precedence. There is no built-in deployment origin.
type Values struct {
	Path             string
	URL              string
	URLSource        string
	Credential       string
	CredentialSource string
	FileLoaded       bool
}

// ResolveOptions controls effective configuration resolution.
type ResolveOptions struct {
	Path         string
	PathRequired bool
	URL          string
	AllowHTTP    bool
	Getenv       func(string) string
}

// DefaultPath returns the platform-specific PCR configuration path.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: determine user configuration directory", ErrInvalid)
	}
	return filepath.Join(dir, "pcr", "config.toml"), nil
}

// BootstrapPath resolves the config path without parsing any other option.
// This lets the full parser and configuration loader share the same path.
func BootstrapPath(args []string, getenv func(string) string) (path string, required bool, err error) {
	configuredPath, found, err := configPathFromArgs(args)
	if err != nil {
		return "", false, err
	}
	if found {
		if configuredPath == "" {
			return "", false, fmt.Errorf("%w: --config requires a non-empty path", ErrInvalid)
		}
		if containsUnsafeText(configuredPath) {
			return "", false, fmt.Errorf("%w: --config path contains control characters", ErrInvalid)
		}
		return configuredPath, true, nil
	}

	if configuredPath = strings.TrimSpace(getenv("PCR_CONFIG")); configuredPath != "" {
		if containsUnsafeText(configuredPath) {
			return "", false, fmt.Errorf("%w: PCR_CONFIG path contains control characters", ErrInvalid)
		}
		return configuredPath, true, nil
	}
	configuredPath, err = DefaultPath()
	if err != nil {
		return "", false, err
	}
	return configuredPath, false, nil
}

// configPathFromArgs scans argv for --config without consulting the full flag
// parser, so a malformed later option cannot hide the config path.
func configPathFromArgs(args []string) (string, bool, error) {
	var configuredPath string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--config":
			if found {
				return "", false, fmt.Errorf("%w: --config may be specified only once", ErrInvalid)
			}
			value, err := separateFlagValue(args, i)
			if err != nil {
				return "", false, err
			}
			// Consume the value so the scan does not revisit it as an argument.
			i++
			configuredPath = value
			found = true
		case strings.HasPrefix(arg, "--config="):
			if found {
				return "", false, fmt.Errorf("%w: --config may be specified only once", ErrInvalid)
			}
			configuredPath = strings.TrimPrefix(arg, "--config=")
			found = true
		}
	}
	return configuredPath, found, nil
}

// separateFlagValue returns the argument following the flag at flagIndex. A
// value that looks like a flag is rejected rather than consumed, so a missing
// path cannot silently swallow the next option.
func separateFlagValue(args []string, flagIndex int) (string, error) {
	if flagIndex+1 >= len(args) || strings.HasPrefix(args[flagIndex+1], "-") {
		return "", fmt.Errorf("%w: --config requires a path", ErrInvalid)
	}
	return args[flagIndex+1], nil
}

// Load reads a strict, size-bounded configuration file. A missing default
// file is equivalent to an empty configuration; a missing explicitly selected
// file is an error.
func Load(path string, required bool) (File, bool, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the config path is operator-selected (--config, PCR_CONFIG, or the user config dir) and screened by BootstrapPath.
	if errors.Is(err, os.ErrNotExist) && !required {
		return File{}, false, nil
	}
	if err != nil {
		return File{}, false, fmt.Errorf("%w: open configuration file %q", ErrInvalid, path)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return File{}, false, fmt.Errorf("%w: read configuration file %q", ErrInvalid, path)
	}
	if len(data) > maxFileSize {
		return File{}, false, fmt.Errorf("%w: configuration file %q exceeds 1 MiB", ErrInvalid, path)
	}

	var keys map[string]any
	if err := toml.Unmarshal(data, &keys); err != nil {
		return File{}, false, fmt.Errorf("%w: configuration file %q is not valid TOML", ErrInvalid, path)
	}
	known := map[string]bool{"version": true, "url": true, "credential": true}
	var unknown []string
	for key := range keys {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		return File{}, false, fmt.Errorf("%w: unknown configuration key %q", ErrInvalid, unknown[0])
	}

	var cfg File
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return File{}, false, fmt.Errorf("%w: configuration file %q has invalid value types", ErrInvalid, path)
	}
	if err := validateFile(cfg); err != nil {
		return File{}, false, err
	}

	info, err := f.Stat()
	if err != nil {
		return File{}, false, fmt.Errorf("%w: inspect configuration file %q", ErrInvalid, path)
	}
	if cfg.Credential != "" && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return File{}, false, fmt.Errorf("%w: configuration file %q contains a credential but mode %04o permits group or other access; run chmod 600 %q", ErrInvalid, path, info.Mode().Perm(), path)
	}
	return cfg, true, nil
}

// Resolve loads the selected file and applies the documented value precedence.
func Resolve(opts ResolveOptions) (Values, error) {
	cfg, loaded, err := Load(opts.Path, opts.PathRequired)
	if err != nil {
		return Values{}, err
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	values := Values{Path: opts.Path, FileLoaded: loaded}
	switch {
	case strings.TrimSpace(opts.URL) != "":
		values.URL = strings.TrimSpace(opts.URL)
		values.URLSource = "flag"
	case strings.TrimSpace(getenv("PCR_URL")) != "":
		values.URL = strings.TrimSpace(getenv("PCR_URL"))
		values.URLSource = "environment"
	case cfg.URL != "":
		values.URL = cfg.URL
		values.URLSource = "file"
	}
	origin, err := ParseOrigin(values.URL, opts.AllowHTTP)
	if err != nil {
		return Values{}, err
	}
	values.URL = origin.String()

	environmentCredential := getenv("PCR_CREDENTIAL")
	switch {
	case environmentCredential != "":
		if err := ValidateCredential(environmentCredential); err != nil {
			return Values{}, err
		}
		values.Credential = environmentCredential
		values.CredentialSource = "environment"
	case cfg.Credential != "":
		values.Credential = cfg.Credential
		values.CredentialSource = "file"
	default:
		values.CredentialSource = "missing"
	}
	return values, nil
}

// ParseOrigin validates and canonicalizes a PCR origin.
func ParseOrigin(raw string, allowHTTP bool) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%w: PCR URL is required; set --url, PCR_URL, or url in the configuration file", ErrInvalid)
	}
	if strings.TrimSpace(raw) != raw {
		return nil, fmt.Errorf("%w: PCR URL must be an HTTPS origin", ErrInvalid)
	}
	origin, err := url.Parse(raw)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.Hostname() == "" || origin.Opaque != "" {
		return nil, fmt.Errorf("%w: PCR URL must be an HTTPS origin", ErrInvalid)
	}
	if origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, fmt.Errorf("%w: PCR URL must not contain user info, a path, query, or fragment", ErrInvalid)
	}
	if origin.Scheme != "https" {
		isHTTPLoopback := origin.Scheme == "http" && isLoopback(origin.Hostname())
		if !allowHTTP || !isHTTPLoopback {
			return nil, fmt.Errorf("%w: PCR URL must use HTTPS; --allow-http permits HTTP only for loopback hosts", ErrInvalid)
		}
	}
	origin.Path = ""
	return origin, nil
}

// ValidateCredential checks the complete Authentik email/app-password value.
func ValidateCredential(credential string) error {
	if credential == "" || strings.TrimSpace(credential) != credential || containsUnsafeText(credential) {
		return fmt.Errorf("%w: credential must be an email and app password separated by a colon", ErrInvalid)
	}
	email, password, found := strings.Cut(credential, ":")
	if !found || email == "" || password == "" || !strings.Contains(email, "@") {
		return fmt.Errorf("%w: credential must be an email and app password separated by a colon", ErrInvalid)
	}
	return nil
}

func validateFile(cfg File) error {
	if cfg.Version != 1 {
		return fmt.Errorf("%w: unsupported configuration version %d", ErrInvalid, cfg.Version)
	}
	if cfg.URL == "" {
		return fmt.Errorf("%w: configuration URL is required", ErrInvalid)
	}
	if _, err := ParseOrigin(cfg.URL, true); err != nil {
		return err
	}
	if cfg.Credential != "" {
		return ValidateCredential(cfg.Credential)
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func containsUnsafeText(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}
