package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
)

// The list_deployments name, description, input schema, limits, error
// messages, and output shape are a contract that agents and their prompts
// rely on. Keep them stable: changing any of them is a breaking change.
const (
	listDeploymentsToolName    = "list_deployments"
	listDeploymentsDescription = "Read PCR's canonical change timeline for any fixed UTC window: deployments, feature flags, Kubernetes changes and maintenance. Use for general change-history questions as well as incident correlation. Returns 25 top-level events per page with tags, descriptions, commit/application links and PCR permalinks. Follow next_offset for complete coverage. Group Argo start/end events by change_id or deploy_id; event timestamp is rollout time. This is read-only. PCR coverage is opt-in, so missing records do not prove no deploy."
	deploymentsCoverage        = "PCR reporting is opt-in and may miss deliveries; absence of recorded changes does not prove no deployment. Offset pagination is not a snapshot: deduplicate event IDs and recheck if counts change."

	deploymentsPageSize  = 25
	deploymentsMaxWindow = 24 * time.Hour
	deploymentsMaxOffset = 10000

	// Oversized text is cut to keep a page within a model's context. The
	// thresholds are bytes and the cut lengths are runes, as before.
	maxDescriptionBytes           = 1000
	truncatedDescriptionRunes     = 500
	maxLongDescriptionBytes       = 4000
	truncatedLongDescriptionRunes = 2000

	mcpMaxRequestBytes = 1 << 20
	// The SDK detaches tool contexts from the HTTP request for protocol
	// versions before 2026-07-28, so the router's request timeout does not
	// reach the store. Bound the query to the same 30 seconds here.
	mcpToolTimeout = 30 * time.Second
)

type deploymentQuery struct {
	StartAfter  string `json:"start_after" jsonschema:"Inclusive RFC3339 UTC start of the change-history window."`
	StartBefore string `json:"start_before" jsonschema:"Exclusive RFC3339 UTC end. Maximum window 24 hours."`
	Service     string `json:"service,omitempty" jsonschema:"Optional exact PCR service tag. Omit initially to include dependency deploys and naming differences."`
	Environment string `json:"environment,omitempty" jsonschema:"Optional exact PCR env tag such as production."`
	Cluster     string `json:"cluster,omitempty" jsonschema:"Optional exact PCR cluster tag."`
	Offset      int    `json:"offset" jsonschema:"Pagination offset; start at 0 and follow next_offset until absent. Deduplicate event IDs."`
}

type deploymentPage struct {
	Events     []deploymentEvent `json:"events"`
	TotalCount int               `json:"total_count"`
	Offset     int               `json:"offset"`
	NextOffset *int              `json:"next_offset,omitempty"`
	Coverage   string            `json:"coverage"`
}

type deploymentEvent struct {
	ID              string            `json:"id"`
	UserName        string            `json:"user_name"`
	Timestamp       time.Time         `json:"timestamp"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Tags            map[string]string `json:"tags,omitempty"`
	Links           []deploymentLink  `json:"links,omitempty"`
	URL             string            `json:"url"`
	Truncated       bool              `json:"description_truncated,omitempty"`
}

type deploymentLink struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

// NewMCPHandler serves PCR's read-only MCP tools over stateless streamable
// HTTP. Every POST is a complete JSON-RPC exchange answered with
// application/json; there are no sessions or server-initiated streams.
// publicURL is the dashboard origin used to build event permalinks.
func NewMCPHandler(svc *service.ChangeService, publicURL string) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "pcr", Version: serverVersion()}, nil)
	deployments := &deploymentsTool{svc: svc, publicURL: strings.TrimRight(publicURL, "/")}
	server.AddTool(listDeploymentsTool(), deployments.call)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: mcpMaxRequestBytes,
		Logger:              slog.Default(),
		// Newer clients bind the call to the POST; cancel it with the request.
		PropagateRequestCancellation: true,
	})
}

func listDeploymentsTool() *mcp.Tool {
	schema, err := jsonschema.For[deploymentQuery](nil)
	if err != nil {
		panic(fmt.Sprintf("infer %s input schema: %v", listDeploymentsToolName, err))
	}
	return &mcp.Tool{
		Name:        listDeploymentsToolName,
		Description: listDeploymentsDescription,
		InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: new(false),
			IdempotentHint:  true,
			OpenWorldHint:   new(false),
		},
	}
}

type deploymentsTool struct {
	svc       *service.ChangeService
	publicURL string
}

// call decodes arguments leniently: unknown fields are ignored and an omitted
// offset means 0, although the advertised schema lists offset as required.
func (t *deploymentsTool) call(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var query deploymentQuery
	if args := req.Params.Arguments; len(args) > 0 {
		if err := json.Unmarshal(args, &query); err != nil {
			return toolError(fmt.Errorf("invalid deployment arguments: %w", err)), nil
		}
	}
	page, err := t.list(ctx, query)
	if err != nil {
		return toolError(err), nil
	}
	text, err := json.Marshal(page)
	if err != nil {
		return nil, fmt.Errorf("encode %s result: %w", listDeploymentsToolName, err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}, nil
}

func (t *deploymentsTool) list(ctx context.Context, query deploymentQuery) (deploymentPage, error) {
	start, err := time.Parse(time.RFC3339Nano, query.StartAfter)
	if err != nil {
		return deploymentPage{}, fmt.Errorf("start_after must be RFC3339: %w", err)
	}
	end, err := time.Parse(time.RFC3339Nano, query.StartBefore)
	if err != nil {
		return deploymentPage{}, fmt.Errorf("start_before must be RFC3339: %w", err)
	}
	if !start.Before(end) || end.Sub(start) > deploymentsMaxWindow || query.Offset < 0 || query.Offset > deploymentsMaxOffset {
		return deploymentPage{}, errors.New("PCR query requires an increasing window of at most 24h and offset 0-10000")
	}
	start, end = start.UTC(), end.UTC()

	ctx, cancel := context.WithTimeout(ctx, mcpToolTimeout)
	defer cancel()
	result, err := t.svc.List(ctx, model.ListParams{
		StartAfter:  &start,
		StartBefore: &end,
		TopLevel:    true,
		Tags:        deploymentTags(query),
		Limit:       deploymentsPageSize,
		Offset:      query.Offset,
	})
	if err != nil {
		slog.ErrorContext(ctx, "list deployments error", "error", err)
		return deploymentPage{}, errors.New("PCR list events: an internal error occurred")
	}
	// The count and the page are separate queries, so they can disagree.
	if len(result.Events) == 0 && query.Offset < result.TotalCount {
		return deploymentPage{}, errors.New("PCR pagination changed during read; retry the fixed window from offset 0")
	}

	page := deploymentPage{
		Events:     make([]deploymentEvent, 0, len(result.Events)),
		TotalCount: result.TotalCount,
		Offset:     query.Offset,
		Coverage:   deploymentsCoverage,
	}
	for _, event := range result.Events {
		page.Events = append(page.Events, t.deploymentEvent(event))
	}
	if next := query.Offset + len(result.Events); next < result.TotalCount {
		page.NextOffset = &next
	}
	return page, nil
}

// deploymentTags maps the tool's optional filters to the PCR tags they match.
func deploymentTags(query deploymentQuery) map[string]string {
	tags := make(map[string]string)
	for key, value := range map[string]string{"service": query.Service, "env": query.Environment, "cluster": query.Cluster} {
		if value != "" {
			tags[key] = value
		}
	}
	return tags
}

func (t *deploymentsTool) deploymentEvent(event model.ChangeEvent) deploymentEvent {
	out := deploymentEvent{
		ID:              event.ID,
		UserName:        event.UserName,
		Timestamp:       event.Timestamp,
		EventType:       event.EventType,
		Description:     event.Description,
		LongDescription: event.LongDescription,
		Tags:            event.Tags,
		URL:             t.publicURL + "/events/" + url.PathEscape(event.ID),
	}
	for _, link := range event.Links {
		out.Links = append(out.Links, deploymentLink{URL: link.URL, Label: link.Label})
	}
	if len(out.Description) > maxDescriptionBytes {
		out.Description = truncateRunes(out.Description, truncatedDescriptionRunes)
		out.Truncated = true
	}
	if len(out.LongDescription) > maxLongDescriptionBytes {
		out.LongDescription = truncateRunes(out.LongDescription, truncatedLongDescriptionRunes)
		out.Truncated = true
	}
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	return string(runes[:min(limit, len(runes))])
}

func toolError(err error) *mcp.CallToolResult {
	var result mcp.CallToolResult
	result.SetError(err)
	return &result
}

// serverVersion reports the module version stamped into the build, which the
// MCP initialize handshake requires.
func serverVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
