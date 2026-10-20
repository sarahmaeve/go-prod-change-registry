package pcrcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrclient"
	"github.com/sarahmaeve/go-prod-change-registry/internal/pcrconfig"
)

// EventsCommand groups event operations.
type EventsCommand struct {
	List        EventsListCommand        `cmd:"" help:"List events."`
	Get         EventsGetCommand         `cmd:"" help:"Get an event."`
	Annotations EventsAnnotationsCommand `cmd:"" help:"Get event annotation state."`
	Activity    EventsActivityCommand    `cmd:"" help:"Get event activity."`
	Create      EventsCreateCommand      `cmd:"" help:"Create an idempotent event."`
}

// EventsListCommand lists filtered events.
type EventsListCommand struct {
	StartAfter  string   `help:"Only events at or after this RFC3339 time."`
	StartBefore string   `help:"Only events before this RFC3339 time."`
	Around      string   `help:"Center an event time window on this RFC3339 time."`
	Window      string   `help:"Window around --around, such as 30m or 1h."`
	User        string   `help:"Filter by user."`
	Type        string   `help:"Filter by event type."`
	Tag         []string `help:"Filter by tag as key=value; repeatable."`
	TopLevel    bool     `help:"Return only top-level events."`
	Alerted     bool     `help:"Return only alerted events."`
	Limit       int      `help:"Maximum results." min:"0" max:"200"`
	Offset      int      `help:"Result offset." min:"0"`
}

func (c *EventsListCommand) Run(rt *Runtime) error {
	startAfter, err := parseOptionalTime("--start-after", c.StartAfter)
	if err != nil {
		return err
	}
	startBefore, err := parseOptionalTime("--start-before", c.StartBefore)
	if err != nil {
		return err
	}
	around, err := parseOptionalTime("--around", c.Around)
	if err != nil {
		return err
	}
	window, err := parseOptionalDuration(c.Window)
	if err != nil {
		return err
	}
	if window != 0 && around == nil {
		return fmt.Errorf("%w: --window requires --around", errUsage)
	}
	tags, err := parseTags(c.Tag)
	if err != nil {
		return err
	}
	client, err := rt.client()
	if err != nil {
		return err
	}
	result, err := client.List(rt.Context, pcrclient.ListOptions{
		StartAfter:  startAfter,
		StartBefore: startBefore,
		Around:      around,
		Window:      window,
		User:        strings.TrimSpace(c.User),
		EventType:   strings.TrimSpace(c.Type),
		Tags:        tags,
		TopLevel:    c.TopLevel,
		AlertedOnly: c.Alerted,
		Limit:       c.Limit,
		Offset:      c.Offset,
	})
	if err != nil {
		return err
	}
	return writeList(rt, result)
}

// EventsGetCommand gets one event.
type EventsGetCommand struct {
	EventID string `arg:"" name:"event-id" help:"Event ID."`
}

func (c *EventsGetCommand) Run(rt *Runtime) error {
	eventID, err := requiredValue("event-id", c.EventID)
	if err != nil {
		return err
	}
	client, err := rt.client()
	if err != nil {
		return err
	}
	event, err := client.Get(rt.Context, eventID)
	if err != nil {
		return err
	}
	return writeValue(rt, event)
}

// EventsAnnotationsCommand gets event annotation state.
type EventsAnnotationsCommand struct {
	EventID string `arg:"" name:"event-id" help:"Event ID."`
}

func (c *EventsAnnotationsCommand) Run(rt *Runtime) error {
	eventID, err := requiredValue("event-id", c.EventID)
	if err != nil {
		return err
	}
	client, err := rt.client()
	if err != nil {
		return err
	}
	annotations, err := client.Annotations(rt.Context, eventID)
	if err != nil {
		return err
	}
	return writeValue(rt, annotations)
}

// EventsActivityCommand gets an event's activity.
type EventsActivityCommand struct {
	EventID string `arg:"" name:"event-id" help:"Event ID."`
}

func (c *EventsActivityCommand) Run(rt *Runtime) error {
	eventID, err := requiredValue("event-id", c.EventID)
	if err != nil {
		return err
	}
	client, err := rt.client()
	if err != nil {
		return err
	}
	events, err := client.Activity(rt.Context, eventID)
	if err != nil {
		return err
	}
	return writeEvents(rt, events)
}

// EventsCreateCommand creates an idempotent event.
type EventsCreateCommand struct {
	ExternalID      string   `help:"Stable producer event ID." required:""`
	Type            string   `help:"Event type." required:""`
	Description     string   `help:"Short event description." required:""`
	LongDescription string   `help:"Detailed event description."`
	Tag             []string `help:"Event tag as key=value; repeatable."`
}

func (c *EventsCreateCommand) Run(rt *Runtime) error {
	externalID, err := requiredValue("--external-id", c.ExternalID)
	if err != nil {
		return err
	}
	eventType, err := requiredValue("--type", c.Type)
	if err != nil {
		return err
	}
	description, err := requiredValue("--description", c.Description)
	if err != nil {
		return err
	}
	tags, err := parseTags(c.Tag)
	if err != nil {
		return err
	}
	client, err := rt.client()
	if err != nil {
		return err
	}
	event, err := client.Create(rt.Context, pcrclient.CreateRequest{
		ExternalID:      externalID,
		EventType:       eventType,
		Description:     description,
		LongDescription: strings.TrimSpace(c.LongDescription),
		Tags:            tags,
	})
	if err != nil {
		return err
	}
	return writeValue(rt, event)
}

// CurrentCommand lists active logical operations.
type CurrentCommand struct {
	Team     string   `help:"Include operations visible to a team."`
	Scope    []string `help:"Filter by scope; repeatable."`
	Severity []string `help:"Filter by severity; repeatable."`
	Type     string   `help:"Filter by event type."`
	Limit    int      `help:"Maximum results." min:"0" max:"200"`
	Offset   int      `help:"Result offset." min:"0"`
}

func (c *CurrentCommand) Run(rt *Runtime) error {
	client, err := rt.client()
	if err != nil {
		return err
	}
	result, err := client.Current(rt.Context, pcrclient.CurrentOptions{
		Team:       strings.TrimSpace(c.Team),
		Scopes:     cleanValues(c.Scope),
		Severities: cleanValues(c.Severity),
		EventType:  strings.TrimSpace(c.Type),
		Limit:      c.Limit,
		Offset:     c.Offset,
	})
	if err != nil {
		return err
	}
	return writeList(rt, result)
}

// DoctorCommand validates configuration and authenticated read access.
type DoctorCommand struct {
	Strict bool `help:"Treat warnings as failures."`
}

func (c *DoctorCommand) Run(rt *Runtime) error {
	result := doctorResult{Status: "ok", Probes: []doctorProbe{}}
	values, err := rt.values()
	if err != nil {
		result.Status = "fail"
		result.Probes = append(result.Probes, doctorProbe{Name: "configuration", Status: "fail", Detail: "fix the reported configuration error"})
		_ = writeValue(rt, result)
		return err
	}
	result.Probes = append(result.Probes, doctorProbe{Name: "configuration", Status: "ok", Detail: "target and credential are configured"})
	if values.Credential == "" {
		result.Status = "fail"
		result.Probes[0] = doctorProbe{Name: "configuration", Status: "fail", Detail: "set PCR_CREDENTIAL or run pcr config set-credential"}
		_ = writeValue(rt, result)
		return fmt.Errorf("%w: credential is missing", pcrconfig.ErrInvalid)
	}
	client, err := rt.client()
	if err != nil {
		result.Status = "fail"
		result.Probes = append(result.Probes, doctorProbe{Name: "access", Status: "fail", Detail: "check the target and credential"})
		_ = writeValue(rt, result)
		return err
	}
	if _, err := client.List(rt.Context, pcrclient.ListOptions{Limit: 1}); err != nil {
		result.Status = "fail"
		result.Probes = append(result.Probes, doctorProbe{Name: "access", Status: "fail", Detail: "check network access and credential authorization"})
		_ = writeValue(rt, result)
		return err
	}
	result.Probes = append(result.Probes, doctorProbe{Name: "access", Status: "ok", Detail: "authenticated read succeeded"})
	return writeValue(rt, result)
}

type doctorResult struct {
	Status string        `json:"status"`
	Probes []doctorProbe `json:"probes"`
}

type doctorProbe struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type configPathResult struct {
	Path string `json:"path"`
}

type configShowResult struct {
	Path             string `json:"path"`
	URL              string `json:"url"`
	URLSource        string `json:"url_source"`
	Credential       string `json:"credential"`
	CredentialSource string `json:"credential_source"`
}

type configWriteResult struct {
	Path       string `json:"path"`
	Credential string `json:"credential"`
}

// ConfigCommand groups configuration operations.
type ConfigCommand struct {
	Path          ConfigPathCommand          `cmd:"" help:"Print the effective configuration path."`
	Show          ConfigShowCommand          `cmd:"" help:"Show redacted effective configuration."`
	Init          ConfigInitCommand          `cmd:"" help:"Create a configuration file without a credential."`
	SetCredential ConfigSetCredentialCommand `cmd:"" help:"Read and store a credential securely."`
}

// ConfigPathCommand prints the selected path without reading it.
type ConfigPathCommand struct{}

func (c *ConfigPathCommand) Run(rt *Runtime) error {
	return writeValue(rt, configPathResult{Path: rt.ConfigPath})
}

// ConfigShowCommand displays effective settings without credential material.
type ConfigShowCommand struct{}

func (c *ConfigShowCommand) Run(rt *Runtime) error {
	values, err := rt.values()
	if err != nil {
		return err
	}
	credential := "missing"
	if values.Credential != "" {
		credential = "configured"
	}
	return writeValue(rt, configShowResult{
		Path:             values.Path,
		URL:              values.URL,
		URLSource:        values.URLSource,
		Credential:       credential,
		CredentialSource: values.CredentialSource,
	})
}

// ConfigInitCommand creates a credential-free configuration.
type ConfigInitCommand struct{}

func (c *ConfigInitCommand) Run(rt *Runtime) error {
	getenv := getenvOrOS(rt.Getenv)
	target := strings.TrimSpace(rt.URL)
	if target == "" {
		target = strings.TrimSpace(getenv("PCR_URL"))
	}
	origin, err := pcrconfig.ParseOrigin(target, rt.AllowHTTP)
	if err != nil {
		return err
	}
	if err := pcrconfig.Write(rt.ConfigPath, pcrconfig.File{Version: 1, URL: origin.String()}, false); err != nil {
		return err
	}
	return writeValue(rt, configWriteResult{Path: rt.ConfigPath, Credential: "missing"})
}

// ConfigSetCredentialCommand stores one credential read from a protected
// terminal prompt or stdin.
type ConfigSetCredentialCommand struct{}

func (c *ConfigSetCredentialCommand) Run(rt *Runtime) error {
	cfg, _, err := pcrconfig.Load(rt.ConfigPath, true)
	if err != nil {
		return err
	}
	credential, err := pcrconfig.ReadCredential(rt.Stdin, rt.Stderr)
	if err != nil {
		return err
	}
	cfg.Credential = credential
	if err := pcrconfig.Write(rt.ConfigPath, cfg, true); err != nil {
		return err
	}
	return writeValue(rt, configWriteResult{Path: rt.ConfigPath, Credential: "configured"})
}

// VersionCommand prints build metadata without loading configuration.
type VersionCommand struct{}

func (c *VersionCommand) Run(rt *Runtime) error {
	return writeValue(rt, rt.Build)
}

func requiredValue(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%w: %s must be non-empty", errUsage, name)
	}
	return value, nil
}

func parseTags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	tags := make(map[string]string, len(values))
	for _, value := range values {
		key, tagValue, found := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return nil, fmt.Errorf("%w: --tag must be key=value", errUsage)
		}
		tags[key] = tagValue
	}
	return tags, nil
}

func parseOptionalTime(name, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fmt.Errorf("%w: %s must be RFC3339", errUsage, name)
	}
	return &parsed, nil
}

func parseOptionalDuration(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%w: --window must be a positive duration", errUsage)
	}
	return duration, nil
}

func cleanValues(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}
