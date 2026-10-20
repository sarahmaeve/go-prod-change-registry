package pcrconfig //nolint:testpackage // white-box tests cover unexported path resolution and file installation

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBootstrapPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		args         []string
		environment  string
		wantPath     string
		wantRequired bool
		wantError    bool
	}{
		{name: "separate flag", args: []string{"version", "--config", "/tmp/one.toml"}, wantPath: "/tmp/one.toml", wantRequired: true},
		{name: "equals flag", args: []string{"--config=/tmp/two.toml", "version"}, wantPath: "/tmp/two.toml", wantRequired: true},
		{name: "environment", environment: "/tmp/env.toml", wantPath: "/tmp/env.toml", wantRequired: true},
		{name: "duplicate", args: []string{"--config=a", "--config", "b"}, wantError: true},
		{name: "missing value", args: []string{"--config", "--output=json"}, wantError: true},
		{name: "empty equals", args: []string{"--config="}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, required, err := BootstrapPath(test.args, func(name string) string {
				if name == "PCR_CONFIG" {
					return test.environment
				}
				return ""
			})
			if test.wantError {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("BootstrapPath() error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BootstrapPath() error = %v", err)
			}
			if test.wantPath != "" && path != test.wantPath {
				t.Errorf("BootstrapPath() path = %q, want %q", path, test.wantPath)
			}
			if required != test.wantRequired {
				t.Errorf("BootstrapPath() required = %t, want %t", required, test.wantRequired)
			}
		})
	}
}

func TestLoadStrictValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		contents  string
		wantError string
	}{
		{name: "valid", contents: "version = 1\nurl = \"https://pcr.example.com\"\ncredential = \"user@example.com:password\"\n"},
		{name: "unknown", contents: "version = 1\nurl = \"https://pcr.example.com\"\nsurprise = true\n", wantError: "unknown configuration key"},
		{name: "version", contents: "version = 2\nurl = \"https://pcr.example.com\"\n", wantError: "unsupported configuration version"},
		{name: "malformed TOML", contents: "version = [\n", wantError: "not valid TOML"},
		{name: "duplicate", contents: "version = 1\nversion = 1\nurl = \"https://pcr.example.com\"\n", wantError: "not valid TOML"},
		{name: "invalid type", contents: "version = \"one\"\nurl = \"https://pcr.example.com\"\n", wantError: "invalid value types"},
		{name: "URL path", contents: "version = 1\nurl = \"https://pcr.example.com/api\"\n", wantError: "must not contain"},
		{name: "credential", contents: "version = 1\nurl = \"https://pcr.example.com\"\ncredential = \"not-a-composite\"\n", wantError: "credential must be"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatalf("WriteFile(): %v", err)
			}
			cfg, loaded, err := Load(path, true)
			if test.wantError != "" {
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Load() error = %v, want ErrInvalid containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !loaded || cfg.Version != 1 || cfg.Credential == "" {
				t.Errorf("Load() = (%+v, %t), want loaded version 1 with credential", cfg, loaded)
			}
		})
	}
}

func TestLoadMissingAndBounded(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.toml")
	if _, loaded, err := Load(path, false); err != nil || loaded {
		t.Fatalf("Load(optional missing) loaded = %t, error = %v", loaded, err)
	}
	if _, _, err := Load(path, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load(required missing) error = %v, want ErrInvalid", err)
	}

	largePath := filepath.Join(t.TempDir(), "large.toml")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("x", maxFileSize+1)), 0o600); err != nil {
		t.Fatalf("WriteFile(large): %v", err)
	}
	if _, _, err := Load(largePath, true); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("Load(large) error = %v, want bounded-input error", err)
	}
}

func TestLoadRefusesUnsafeCredentialModeWithoutLeaking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	t.Parallel()
	const credential = "person@example.com:top-secret-value"
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "version = 1\nurl = \"https://pcr.example.com\"\ncredential = \"" + credential + "\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil { //nolint:gosec // G306: the group-readable mode is the fixture under test; Load must refuse it.
		t.Fatalf("WriteFile(): %v", err)
	}
	_, _, err := Load(path, true)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("Load() error = %v, want chmod guidance", err)
	}
	if strings.Contains(err.Error(), credential) || strings.Contains(err.Error(), "top-secret") {
		t.Fatal("Load() error disclosed credential material")
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\nurl = \"https://file.example.com\"\ncredential = \"file@example.com:file-password\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}
	tests := []struct {
		name             string
		flagURL          string
		envURL           string
		envCredential    string
		wantURL          string
		wantURLSource    string
		wantCredential   string
		wantCredentialAt string
	}{
		//nolint:gosec // G101: example.com test fixture, not a real credential.
		{name: "flag and environment", flagURL: "https://flag.example.com", envURL: "https://env.example.com", envCredential: "env@example.com:env-password", wantURL: "https://flag.example.com", wantURLSource: "flag", wantCredential: "env@example.com:env-password", wantCredentialAt: "environment"},
		//nolint:gosec // G101: example.com test fixture, not a real credential.
		{name: "environment", envURL: "https://env.example.com", envCredential: "env@example.com:env-password", wantURL: "https://env.example.com", wantURLSource: "environment", wantCredential: "env@example.com:env-password", wantCredentialAt: "environment"},
		{name: "file", wantURL: "https://file.example.com", wantURLSource: "file", wantCredential: "file@example.com:file-password", wantCredentialAt: "file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values, err := Resolve(ResolveOptions{
				Path:         path,
				PathRequired: true,
				URL:          test.flagURL,
				Getenv: func(name string) string {
					switch name {
					case "PCR_URL":
						return test.envURL
					case "PCR_CREDENTIAL":
						return test.envCredential
					default:
						return ""
					}
				},
			})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if values.URL != test.wantURL || values.URLSource != test.wantURLSource {
				t.Errorf("Resolve() URL = %q from %q, want %q from %q", values.URL, values.URLSource, test.wantURL, test.wantURLSource)
			}
			if values.Credential != test.wantCredential || values.CredentialSource != test.wantCredentialAt {
				t.Errorf("Resolve() credential source = %q, want %q", values.CredentialSource, test.wantCredentialAt)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing.toml")
	_, err := Resolve(ResolveOptions{Path: missing, Getenv: func(string) string { return "" }})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "PCR URL is required") {
		t.Fatalf("Resolve(no URL) error = %v, want configuration guidance", err)
	}
	values, err := Resolve(ResolveOptions{Path: missing, URL: "https://explicit.example.com", Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatalf("Resolve(explicit URL) error = %v", err)
	}
	if values.URL != "https://explicit.example.com" || values.URLSource != "flag" || values.CredentialSource != "missing" {
		t.Errorf("Resolve(explicit URL) = %+v", values)
	}
}

func TestParseOrigin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		allowHTTP bool
		want      string
	}{
		{name: "HTTPS", raw: "https://pcr.example.com/", want: "https://pcr.example.com"},
		{name: "loopback", raw: "http://127.0.0.1:8080", allowHTTP: true, want: "http://127.0.0.1:8080"},
		{name: "localhost", raw: "http://localhost:8080", allowHTTP: true, want: "http://localhost:8080"},
		{name: "remote HTTP", raw: "http://pcr.example.com", allowHTTP: true},
		{name: "HTTP disabled", raw: "http://localhost:8080"},
		{name: "userinfo", raw: "https://user@example.com"},
		{name: "path", raw: "https://pcr.example.com/api"},
		{name: "query", raw: "https://pcr.example.com?secret=value"},
		{name: "fragment", raw: "https://pcr.example.com/#frag"},
		{name: "relative", raw: "pcr.example.com"},
		{name: "whitespace", raw: " https://pcr.example.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			origin, err := ParseOrigin(test.raw, test.allowHTTP)
			if test.want == "" {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("ParseOrigin(%q) error = %v, want ErrInvalid", test.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOrigin(%q) error = %v", test.raw, err)
			}
			if got := origin.String(); got != test.want {
				t.Errorf("ParseOrigin(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestWriteAndReadCredential(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	cfg := File{Version: 1, URL: "https://pcr.example.com"}
	if err := Write(path, cfg, false); err != nil {
		t.Fatalf("Write(create): %v", err)
	}
	if err := Write(path, cfg, false); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Write(no replace) error = %v, want already exists", err)
	}
	credential, err := ReadCredential(strings.NewReader("person@example.com:password\r\n"), &strings.Builder{})
	if err != nil {
		t.Fatalf("ReadCredential() error = %v", err)
	}
	cfg.Credential = credential
	if err := Write(path, cfg, true); err != nil {
		t.Fatalf("Write(replace): %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(): %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("configuration mode = %04o, want 0600", info.Mode().Perm())
	}
	loaded, _, err := Load(path, true)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if loaded.Credential != credential {
		t.Error("stored credential did not round-trip")
	}
}

func FuzzBootstrapPath(f *testing.F) {
	f.Add("--config=/tmp/config.toml", "")
	f.Add("--config\x00bad", "/tmp/env.toml")
	f.Fuzz(func(t *testing.T, arg, environment string) {
		_, _, _ = BootstrapPath([]string{arg}, func(string) string { return environment })
	})
}

func FuzzParseOrigin(f *testing.F) {
	f.Add("https://pcr.example.com", false)
	f.Add("http://127.0.0.1:8080", true)
	f.Add("https://example.com/\x1b[31m", false)
	f.Fuzz(func(t *testing.T, raw string, allowHTTP bool) {
		_, _ = ParseOrigin(raw, allowHTTP)
	})
}

func FuzzValidateCredential(f *testing.F) {
	f.Add("person@example.com:password")
	f.Add("\x1b[31mnot-a-credential")
	f.Fuzz(func(t *testing.T, credential string) {
		err := ValidateCredential(credential)
		const safeError = "invalid PCR configuration: credential must be an email and app password separated by a colon"
		if err != nil && err.Error() != safeError {
			t.Fatalf("ValidateCredential() error = %q, want constant non-disclosing diagnostic", err)
		}
	})
}

func FuzzLoad(f *testing.F) {
	f.Add([]byte("version = 1\nurl = \"https://pcr.example.com\"\n"))
	f.Add([]byte("credential = \"secret@example.com:password\"\n\x00"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFileSize+1 {
			data = data[:maxFileSize+1]
		}
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile(): %v", err)
		}
		_, _, _ = Load(path, true)
	})
}
