package pcrcli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/alecthomas/kong"
)

func TestHelpUsesCompleteGroupedCommandTree(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	code := Run(t.Context(), []string{"--help"}, emptyEnvironment, strings.NewReader(""), &stdout, &stderr, BuildInfo{})
	if code != exitOK || stderr.Len() != 0 {
		t.Fatalf("Run(--help) = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{
		"Work commands",
		"events list",
		"events get <event-id>",
		"events create",
		"current",
		"Setup commands",
		"doctor",
		"config set-credential",
		"version",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help output missing %q", want)
		}
	}
}

func TestVersionAndConfigPathDoNotReadConfiguration(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist.toml")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "version", args: []string{"--config", missing, "version"}, want: `"version":"v1.2.3"`},
		{name: "path", args: []string{"--config", missing, "config", "path"}, want: `"path":"` + missing + `"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr strings.Builder
			code := Run(t.Context(), test.args, emptyEnvironment, strings.NewReader(""), &stdout, &stderr, BuildInfo{Version: "v1.2.3", Commit: "abc", Date: "today"})
			if code != exitOK || stderr.Len() != 0 {
				t.Fatalf("Run(%v) = %d, stderr = %q", test.args, code, stderr.String())
			}
			if !strings.Contains(stdout.String(), test.want) {
				t.Errorf("Run(%v) stdout = %q, want %q", test.args, stdout.String(), test.want)
			}
		})
	}
}

func TestConfigLifecycleNeverPrintsCredential(t *testing.T) {
	t.Parallel()
	const credential = "person@example.com:secret-app-password"
	path := filepath.Join(t.TempDir(), "pcr", "config.toml")
	run := func(args []string, stdin string) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		allArgs := append([]string{"--config", path}, args...)
		code := Run(t.Context(), allArgs, emptyEnvironment, strings.NewReader(stdin), &stdout, &stderr, BuildInfo{})
		return code, stdout.String(), stderr.String()
	}

	code, stdout, stderr := run([]string{"config", "init"}, "")
	if code != exitOK {
		t.Fatalf("config init = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, `"credential":"missing"`) {
		t.Errorf("config init stdout = %q", stdout)
	}

	code, stdout, stderr = run([]string{"config", "set-credential"}, credential+"\n")
	if code != exitOK {
		t.Fatalf("config set-credential = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout+stderr, credential) || strings.Contains(stdout+stderr, "secret-app-password") {
		t.Fatal("config set-credential disclosed credential")
	}

	code, stdout, stderr = run([]string{"config", "show"}, "")
	if code != exitOK {
		t.Fatalf("config show = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, `"credential":"configured"`) || !strings.Contains(stdout, `"credential_source":"file"`) {
		t.Errorf("config show stdout = %q", stdout)
	}
	if strings.Contains(stdout+stderr, credential) || strings.Contains(stdout+stderr, "secret-app-password") {
		t.Fatal("config show disclosed credential")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(): %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestEventsCreateEndToEnd(t *testing.T) {
	t.Parallel()
	const credential = "person@example.com:secret-app-password"
	var payload map[string]any
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/events" {
			t.Errorf("request path = %q, want /api/v1/events", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("request Authorization did not use configured credential")
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"id":"event-1","external_id":"build-1","user_name":"person@example.com","event_type":"deployment","description":"deployed"}`)),
			Request:    request,
		}, nil
	})}

	getenv := func(name string) string {
		if name == "PCR_CREDENTIAL" {
			return credential
		}
		return ""
	}
	var stdout, stderr strings.Builder
	runtime := &Runtime{
		Context:    t.Context(),
		Stdin:      strings.NewReader(""),
		Stdout:     &stdout,
		Stderr:     &stderr,
		Getenv:     getenv,
		Build:      BuildInfo{Version: "test"},
		ConfigPath: filepath.Join(t.TempDir(), "missing.toml"),
		URL:        "https://pcr.example.com",
		Timeout:    15,
		Output:     "json",
		HTTPClient: httpClient,
	}
	command := EventsCreateCommand{
		ExternalID:  "build-1",
		Type:        "deployment",
		Description: "deployed",
		Tag:         []string{"env=prod"},
	}
	if err := command.Run(runtime); err != nil {
		t.Fatalf("EventsCreateCommand.Run() error = %v", err)
	}
	if _, ok := payload["user_name"]; ok {
		t.Errorf("request payload contains user_name: %#v", payload)
	}
	if strings.Contains(stdout.String()+stderr.String(), credential) || strings.Contains(stdout.String()+stderr.String(), "secret-app-password") {
		t.Fatal("events create disclosed credential")
	}
	if !strings.Contains(stdout.String(), `"id":"event-1"`) {
		t.Errorf("events create stdout = %q", stdout.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestInputDiagnosticsAreSingleLineAndControlSafe(t *testing.T) {
	t.Parallel()
	malicious := "unknown\n\x1b[31m\tcommand"
	var stdout, stderr strings.Builder
	code := Run(context.Background(), []string{malicious}, emptyEnvironment, strings.NewReader(""), &stdout, &stderr, BuildInfo{})
	if code != exitUsage {
		t.Fatalf("Run(malicious) = %d, want %d", code, exitUsage)
	}
	if containsControl(strings.TrimSuffix(stderr.String(), "\n")) {
		t.Fatalf("stderr contains control characters: %q", stderr.String())
	}
	if strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want exactly one diagnostic line", stderr.String())
	}
	if !strings.Contains(stderr.String(), `\n`) || !strings.Contains(stderr.String(), `\x1b`) {
		t.Errorf("stderr = %q, want escaped control characters", stderr.String())
	}
}

func TestCredentialCannotBePassedAsAFlagOrEchoed(t *testing.T) {
	t.Parallel()
	const credential = "person@example.com:do-not-echo"
	var stdout, stderr strings.Builder
	code := Run(t.Context(), []string{"--credential", credential, "version"}, emptyEnvironment, strings.NewReader(""), &stdout, &stderr, BuildInfo{})
	if code != exitUsage {
		t.Fatalf("Run(--credential) = %d, want %d", code, exitUsage)
	}
	if strings.Contains(stdout.String()+stderr.String(), credential) || strings.Contains(stdout.String()+stderr.String(), "do-not-echo") {
		t.Fatal("unsupported credential flag echoed credential value")
	}
}

func TestMissingCredentialUsesConfigurationExitCode(t *testing.T) {
	t.Parallel()
	var stdout, stderr strings.Builder
	missing := filepath.Join(t.TempDir(), "missing.toml")
	code := Run(t.Context(), []string{"--config", missing, "events", "get", "event-1"}, emptyEnvironment, strings.NewReader(""), &stdout, &stderr, BuildInfo{})
	if code != exitConfig {
		t.Fatalf("Run(missing credential) = %d, stderr = %q, want %d", code, stderr.String(), exitConfig)
	}
}

func TestTableCellRemovesLayoutAndTerminalControls(t *testing.T) {
	t.Parallel()
	got := tableCell("first\tsecond\n\x1b[31m")
	if containsControl(got) || strings.ContainsAny(got, "\t\n\r") {
		t.Errorf("tableCell() = %q, want control-safe text", got)
	}
}

func FuzzSanitizeDiagnostic(f *testing.F) {
	f.Add("ordinary error")
	f.Add("line one\n\x1b[31mline two\t")
	f.Fuzz(func(t *testing.T, input string) {
		if containsControl(sanitizeDiagnostic(input)) {
			t.Fatal("sanitizeDiagnostic() retained a control character")
		}
	})
}

func FuzzCLIParser(f *testing.F) {
	f.Add("version")
	f.Add("\x1b[31m\nunknown")
	f.Add("--credential=secret")
	f.Fuzz(func(t *testing.T, arg string) {
		var cli CLI
		parser, err := kong.New(&cli, KongOptions(BuildInfo{}, "/tmp/pcr-fuzz.toml", io.Discard, io.Discard, func(int) {})...)
		if err != nil {
			t.Fatalf("kong.New(): %v", err)
		}
		_, parseErr := parser.Parse([]string{arg})
		if parseErr != nil && containsControl(sanitizeDiagnostic(parseErr.Error())) {
			t.Fatal("sanitized parser error retained a control character")
		}
	})
}

func emptyEnvironment(string) string { return "" }

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return true
		}
	}
	return false
}
