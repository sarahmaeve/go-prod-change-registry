// Package pcrcli implements the pcr command-line interface.
package pcrcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alecthomas/kong"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrclient"
	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrconfig"
)

const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 64
	exitNoInput     = 66
	exitUnavailable = 69
	exitNoPerm      = 77
	exitConfig      = 78
)

var errUsage = errors.New("invalid command invocation")

// BuildInfo is release metadata injected by the process entry point.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"build_date"`
}

// CLI is pcr's root Kong model.
type CLI struct {
	Config    string        `help:"Configuration file." type:"path" default:"${config_path}"`
	URL       string        `help:"PCR origin; required here, in PCR_URL, or in the configuration file."`
	AllowHTTP bool          `help:"Allow HTTP for loopback development targets only."`
	Timeout   time.Duration `help:"HTTP request timeout." default:"15s"`
	Output    string        `help:"Output format." enum:"json,jsonl,table" default:"json"`

	Events        EventsCommand  `cmd:"" group:"work" help:"Query and create events."`
	Current       CurrentCommand `cmd:"" group:"work" help:"List active operations."`
	Doctor        DoctorCommand  `cmd:"" group:"setup" help:"Validate configuration and access."`
	ConfigCommand ConfigCommand  `cmd:"" name:"config" group:"setup" help:"Manage configuration."`
	Version       VersionCommand `cmd:"" group:"setup" help:"Print version information."`
}

// Runtime contains process dependencies shared by commands.
type Runtime struct {
	Context      context.Context
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	Getenv       func(string) string
	Build        BuildInfo
	ConfigPath   string
	PathRequired bool
	URL          string
	AllowHTTP    bool
	Timeout      time.Duration
	Output       string
	HTTPClient   *http.Client
}

// KongOptions returns the parser options shared by production and tests.
func KongOptions(build BuildInfo, configPath string, stdout, stderr io.Writer, exit func(int)) []kong.Option {
	return []kong.Option{
		kong.Name("pcr"),
		kong.Description("Query and record production changes in PCR."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(exit),
		kong.Vars{
			"config_path": configPath,
			"version":     displayVersion(build.Version),
		},
		kong.ExplicitGroups([]kong.Group{
			{Key: "work", Title: "Work commands"},
			{Key: "setup", Title: "Setup commands"},
		}),
	}
}

// Run parses and executes one pcr invocation.
func Run(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer, build BuildInfo) int {
	getenv = getenvOrOS(getenv)
	configPath, pathRequired, err := pcrconfig.BootstrapPath(args, getenv)
	if err != nil {
		writeDiagnostic(stderr, fmt.Errorf("%w: %v", errUsage, err))
		return exitUsage
	}

	var cli CLI
	exitRequested := false
	exitCode := exitOK
	parser, err := kong.New(&cli, KongOptions(build, configPath, stdout, stderr, func(code int) {
		exitRequested = true
		exitCode = code
	})...)
	if err != nil {
		writeDiagnostic(stderr, fmt.Errorf("construct command parser: %w", err))
		return exitFailure
	}
	commandContext, err := parser.Parse(args)
	if exitRequested {
		return exitCode
	}
	if err != nil {
		writeDiagnostic(stderr, fmt.Errorf("%w: %v", errUsage, err))
		return exitUsage
	}

	runtime := &Runtime{
		Context:      ctx,
		Stdin:        stdin,
		Stdout:       stdout,
		Stderr:       stderr,
		Getenv:       getenv,
		Build:        normalizedBuild(build),
		ConfigPath:   cli.Config,
		PathRequired: pathRequired,
		URL:          cli.URL,
		AllowHTTP:    cli.AllowHTTP,
		Timeout:      cli.Timeout,
		Output:       cli.Output,
	}
	if err := commandContext.Run(runtime); err != nil {
		writeDiagnostic(stderr, err)
		return exitCodeFor(err)
	}
	return exitOK
}

func (rt *Runtime) values() (pcrconfig.Values, error) {
	return pcrconfig.Resolve(pcrconfig.ResolveOptions{
		Path:         rt.ConfigPath,
		PathRequired: rt.PathRequired,
		URL:          rt.URL,
		AllowHTTP:    rt.AllowHTTP,
		Getenv:       rt.Getenv,
	})
}

func (rt *Runtime) client() (*pcrclient.Client, error) {
	values, err := rt.values()
	if err != nil {
		return nil, err
	}
	origin, err := pcrconfig.ParseOrigin(values.URL, rt.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if values.Credential == "" {
		return nil, fmt.Errorf("%w: credential is missing; set PCR_CREDENTIAL or run pcr config set-credential", pcrconfig.ErrInvalid)
	}
	httpClient := rt.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: rt.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return pcrclient.New(pcrclient.Options{
		Origin:     origin,
		Credential: values.Credential,
		HTTPClient: httpClient,
		UserAgent:  "pcr/" + displayVersion(rt.Build.Version),
	})
}

func exitCodeFor(err error) int {
	if errors.Is(err, errUsage) {
		return exitUsage
	}
	if errors.Is(err, pcrconfig.ErrInvalid) {
		return exitConfig
	}
	var clientError *pcrclient.Error
	if !errors.As(err, &clientError) {
		return exitFailure
	}
	switch clientError.Kind {
	case pcrclient.ErrorNotFound:
		return exitNoInput
	case pcrclient.ErrorUnavailable:
		return exitUnavailable
	case pcrclient.ErrorPermission:
		return exitNoPerm
	case pcrclient.ErrorRequest:
		return exitUsage
	default:
		return exitFailure
	}
}

func writeDiagnostic(w io.Writer, err error) {
	_, _ = fmt.Fprintf(w, "pcr: %s\n", sanitizeDiagnostic(err.Error()))
}

func sanitizeDiagnostic(message string) string {
	var sanitized strings.Builder
	for _, r := range message {
		if !isUnsafeTextRune(r) {
			sanitized.WriteRune(r)
			continue
		}
		quoted := strconv.QuoteRuneToASCII(r)
		sanitized.WriteString(strings.Trim(quoted, "'"))
	}
	return sanitized.String()
}

func isUnsafeTextRune(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

func normalizedBuild(build BuildInfo) BuildInfo {
	build.Version = displayVersion(build.Version)
	if build.Commit == "" {
		build.Commit = "unknown"
	}
	if build.Date == "" {
		build.Date = "unknown"
	}
	return build
}

func displayVersion(version string) string {
	if version == "" {
		return "devel"
	}
	return version
}

func getenvOrOS(getenv func(string) string) func(string) string {
	if getenv == nil {
		return os.Getenv
	}
	return getenv
}
