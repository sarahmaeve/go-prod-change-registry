package pcrconfig_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrconfig"
)

func TestBootstrapPathPreservesArgumentAndEnvironmentPrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "separate flag wins", args: []string{"--config", "chosen.toml"}, want: "chosen.toml"},
		{name: "equals flag wins", args: []string{"--config=chosen.toml"}, want: "chosen.toml"},
		{name: "later malformed option", args: []string{"--config", "chosen.toml", "--url"}, want: "chosen.toml"},
		{name: "earlier unknown option", args: []string{"--unknown", "--config=chosen.toml"}, want: "chosen.toml"},
		{name: "literal equals in value", args: []string{"--config=chosen=one.toml"}, want: "chosen=one.toml"},
		{name: "explicit leading dash", args: []string{"--config=-chosen.toml"}, want: "-chosen.toml"},
		{name: "explicit spaces preserved", args: []string{"--config", " chosen.toml "}, want: " chosen.toml "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, required, err := pcrconfig.BootstrapPath(test.args, func(string) string {
				t.Fatal("explicit config must not consult the environment")
				return ""
			})
			if err != nil || path != test.want || !required {
				t.Fatalf("BootstrapPath() = (%q, %t, %v), want (%q, true, nil)", path, required, err, test.want)
			}
		})
	}
	t.Run("trimmed environment", func(t *testing.T) {
		t.Parallel()
		path, required, err := pcrconfig.BootstrapPath([]string{"--configuration=ignored.toml"}, func(key string) string {
			if key != "PCR_CONFIG" {
				t.Fatalf("unexpected environment key %q", key)
			}
			return "  environment.toml  "
		})
		if err != nil || path != "environment.toml" || !required {
			t.Fatalf("BootstrapPath() = (%q, %t, %v), want environment.toml, true, nil", path, required, err)
		}
	})
}

func TestBootstrapPathRejectsInvalidFlagsWithoutEnvironmentFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing argument", args: []string{"--config"}, want: "--config requires a path"},
		{name: "following option", args: []string{"--config", "--url=x"}, want: "--config requires a path"},
		{name: "leading dash", args: []string{"--config", "-chosen.toml"}, want: "--config requires a path"},
		{name: "empty separate", args: []string{"--config", ""}, want: "--config requires a non-empty path"},
		{name: "empty equals", args: []string{"--config="}, want: "--config requires a non-empty path"},
		{name: "duplicate separate", args: []string{"--config", "a", "--config", "b"}, want: "--config may be specified only once"},
		{name: "duplicate equals", args: []string{"--config=a", "--config=b"}, want: "--config may be specified only once"},
		{name: "duplicate mixed", args: []string{"--config", "a", "--config=b"}, want: "--config may be specified only once"},
		{name: "duplicate before missing value", args: []string{"--config=a", "--config"}, want: "--config may be specified only once"},
		{name: "control character", args: []string{"--config=secret\nvalue"}, want: "--config path contains control characters"},
		{name: "unicode format character", args: []string{"--config", "secret\u202evalue"}, want: "--config path contains control characters"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, required, err := pcrconfig.BootstrapPath(test.args, func(string) string {
				t.Fatal("invalid explicit config must not fall back to the environment")
				return ""
			})
			if !errors.Is(err, pcrconfig.ErrInvalid) || err.Error() != pcrconfig.ErrInvalid.Error()+": "+test.want {
				t.Fatalf("BootstrapPath() error = %v, want ErrInvalid with %q", err, test.want)
			}
			if path != "" || required {
				t.Fatalf("BootstrapPath() returned usable path %q, required %t with error", path, required)
			}
		})
	}
}

func TestWritePreservesExistingFileAndReplacesSecurely(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := pcrconfig.File{Version: 1, URL: "https://original.example.com", Credential: "original@example.com:original-fixture"}
	updated := pcrconfig.File{Version: 1, URL: "https://updated.example.com", Credential: "updated@example.com:updated-fixture"}
	if err := pcrconfig.Write(path, original, false); err != nil {
		t.Fatalf("Write(original): %v", err)
	}
	before, err := os.ReadFile(path) //nolint:gosec // G304: path is a fixed filename in this test's temporary directory.
	if err != nil {
		t.Fatalf("ReadFile(original): %v", err)
	}
	if err := pcrconfig.Write(path, updated, false); !errors.Is(err, pcrconfig.ErrInvalid) || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Write(no replace) error = %v, want ErrInvalid already exists", err)
	}
	after, err := os.ReadFile(path) //nolint:gosec // G304: path is a fixed filename in this test's temporary directory.
	if err != nil || string(after) != string(before) {
		t.Fatalf("Write(no replace) changed existing file; read error = %v", err)
	}
	assertOnlyConfigFile(t, dir)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the permissive fixture verifies replacement restores credential permissions.
			t.Fatalf("Chmod(original): %v", err)
		}
	}
	if err := pcrconfig.Write(path, updated, true); err != nil {
		t.Fatalf("Write(replace): %v", err)
	}
	assertStoredConfig(t, path, updated)
	assertOnlyConfigFile(t, dir)
}

func TestWriteRemovesTemporaryFileWhenInstallationFails(t *testing.T) {
	t.Parallel()
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "replace"}[replace], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatalf("Mkdir(destination): %v", err)
			}
			cfg := pcrconfig.File{Version: 1, URL: "https://pcr.example.com", Credential: "person@example.com:private-fixture"}
			err := pcrconfig.Write(path, cfg, replace)
			if !errors.Is(err, pcrconfig.ErrInvalid) {
				t.Fatalf("Write(directory destination) error = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), cfg.Credential) {
				t.Fatal("Write() error disclosed the credential")
			}
			assertOnlyConfigFile(t, dir)
			info, err := os.Stat(path)
			if err != nil || !info.IsDir() {
				t.Fatalf("destination directory changed; Stat error = %v", err)
			}
		})
	}
}

func TestWriteDoesNotFollowDestinationSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "replace"}[replace], func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			victim := filepath.Join(t.TempDir(), "victim")
			const contents = "existing destination contents"
			if err := os.WriteFile(victim, []byte(contents), 0o600); err != nil {
				t.Fatalf("WriteFile(victim): %v", err)
			}
			path := filepath.Join(dir, "config.toml")
			if err := os.Symlink(victim, path); err != nil {
				t.Fatalf("Symlink(): %v", err)
			}
			cfg := pcrconfig.File{Version: 1, URL: "https://pcr.example.com", Credential: "person@example.com:private-fixture"}
			err := pcrconfig.Write(path, cfg, replace)
			if replace {
				if err != nil {
					t.Fatalf("Write(replace symlink): %v", err)
				}
				assertStoredConfig(t, path, cfg)
			} else if !errors.Is(err, pcrconfig.ErrInvalid) || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("Write(existing symlink) error = %v, want already exists", err)
			}
			data, err := os.ReadFile(victim) //nolint:gosec // G304: victim is a fixed filename in this test's temporary directory.
			if err != nil || string(data) != contents {
				t.Fatalf("symlink target changed; read error = %v", err)
			}
			assertOnlyConfigFile(t, dir)
		})
	}
}

func TestConcurrentWritesCreateExactlyOneCompleteFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	const writers = 16
	results := make(chan error, writers)
	start := make(chan struct{})
	var workers sync.WaitGroup
	cfg := pcrconfig.File{Version: 1, URL: "https://pcr.example.com", Credential: "person@example.com:concurrent-fixture"}
	for range writers {
		workers.Go(func() {
			<-start
			results <- pcrconfig.Write(path, cfg, false)
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, pcrconfig.ErrInvalid) || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("Write() unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful writes = %d, want exactly 1", successes)
	}
	assertStoredConfig(t, path, cfg)
	assertOnlyConfigFile(t, dir)
}

func assertStoredConfig(t *testing.T, path string, want pcrconfig.File) {
	t.Helper()
	got, loaded, err := pcrconfig.Load(path, true)
	if err != nil || !loaded || got != want {
		t.Fatalf("stored configuration did not round-trip; loaded = %t, error = %v", loaded, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(): %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("stored configuration mode = %v, want a regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("stored configuration mode = %04o, want 0600", info.Mode().Perm())
	}
}

func assertOnlyConfigFile(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(): %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("configuration directory contains temporary files: %v", entries)
	}
}
