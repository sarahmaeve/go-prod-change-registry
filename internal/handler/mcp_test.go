package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
)

const mcpTestPublicURL = "https://changes.example.com"

// The tool contract models already know. Changing it is a breaking change for
// every MCP client that attaches PCR.
const wantListDeploymentsDescription = "Read PCR's canonical change timeline for any fixed UTC window: deployments, feature flags, Kubernetes changes and maintenance. Use for general change-history questions as well as incident correlation. Returns 25 top-level events per page with tags, descriptions, commit/application links and PCR permalinks. Follow next_offset for complete coverage. Group Argo start/end events by change_id or deploy_id; event timestamp is rollout time. This is read-only. PCR coverage is opt-in, so missing records do not prove no deploy."

const wantListDeploymentsSchema = `{
	"type": "object",
	"properties": {
		"start_after": {"type": "string", "description": "Inclusive RFC3339 UTC start of the change-history window."},
		"start_before": {"type": "string", "description": "Exclusive RFC3339 UTC end. Maximum window 24 hours."},
		"service": {"type": "string", "description": "Optional exact PCR service tag. Omit initially to include dependency deploys and naming differences."},
		"environment": {"type": "string", "description": "Optional exact PCR env tag such as production."},
		"cluster": {"type": "string", "description": "Optional exact PCR cluster tag."},
		"offset": {"type": "integer", "description": "Pagination offset; start at 0 and follow next_offset until absent. Deduplicate event IDs."}
	},
	"required": ["start_after", "start_before", "offset"],
	"additionalProperties": false
}`

const wantCoverage = "PCR reporting is opt-in and may miss deliveries; absence of recorded changes does not prove no deployment. Offset pagination is not a snapshot: deduplicate event IDs and recheck if counts change."

// newMCPTestHandler serves the MCP handler over a mock store. The public URL
// carries a trailing slash to prove permalinks do not double it.
func newMCPTestHandler(listFn func(context.Context, model.ListParams) (*model.ListResult, error)) http.Handler {
	ms := &mockStore{listFn: listFn}
	return handler.NewMCPHandler(service.NewChangeService(ms), mcpTestPublicURL+"/")
}

func unexpectedList(t *testing.T) func(context.Context, model.ListParams) (*model.ListResult, error) {
	t.Helper()
	return func(context.Context, model.ListParams) (*model.ListResult, error) {
		t.Error("store was called for invalid arguments")
		return nil, errors.New("unexpected List call")
	}
}

// mcpResult sends one JSON-RPC request with the 2025-06-18 protocol and
// returns its result object.
func mcpResult(t *testing.T, h http.Handler, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200; body = %s", method, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s Content-Type = %q, want application/json", method, ct)
	}
	var response struct {
		Result map[string]any  `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %s response: %v; body = %s", method, err, rec.Body.String())
	}
	if response.Error != nil {
		t.Fatalf("%s JSON-RPC error: %s", method, response.Error)
	}
	return response.Result
}

// callListDeployments returns the single text block of a tools/call result
// and whether the result is a tool error.
func callListDeployments(t *testing.T, h http.Handler, arguments any) (string, bool) {
	t.Helper()
	result := mcpResult(t, h, "tools/call", map[string]any{"name": "list_deployments", "arguments": arguments})
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one block", result["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("content block = %#v, want text", block)
	}
	if _, ok := result["structuredContent"]; ok {
		t.Errorf("result has structuredContent; the tool returns text only")
	}
	text, _ := block["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func listDeploymentsPage(t *testing.T, h http.Handler, arguments any) map[string]any {
	t.Helper()
	text, isError := callListDeployments(t, h, arguments)
	if isError {
		t.Fatalf("tools/call isError = true: %s", text)
	}
	var page map[string]any
	if err := json.Unmarshal([]byte(text), &page); err != nil {
		t.Fatalf("decode page: %v; text = %s", err, text)
	}
	return page
}

func windowArguments(offset int) map[string]any {
	return map[string]any{
		"start_after":  "2026-10-14T18:00:00Z",
		"start_before": "2026-10-14T21:00:00Z",
		"offset":       offset,
	}
}

func TestMCPToolsListServesListDeployments(t *testing.T) {
	t.Parallel()

	result := mcpResult(t, newMCPTestHandler(unexpectedList(t)), "tools/list", map[string]any{})
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want exactly list_deployments", result["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "list_deployments" {
		t.Errorf("name = %v, want list_deployments", tool["name"])
	}
	if tool["description"] != wantListDeploymentsDescription {
		t.Errorf("description = %q, want the established contract", tool["description"])
	}
	wantAnnotations := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	if !reflect.DeepEqual(tool["annotations"], wantAnnotations) {
		t.Errorf("annotations = %#v, want %#v", tool["annotations"], wantAnnotations)
	}
	var wantSchema any
	if err := json.Unmarshal([]byte(wantListDeploymentsSchema), &wantSchema); err != nil {
		t.Fatalf("decode expected schema: %v", err)
	}
	if !reflect.DeepEqual(tool["inputSchema"], wantSchema) {
		t.Errorf("inputSchema = %#v, want %s", tool["inputSchema"], wantListDeploymentsSchema)
	}
	if _, ok := tool["outputSchema"]; ok {
		t.Errorf("tool advertises an outputSchema; the result is a JSON text block")
	}
}

func TestMCPListDeploymentsReturnsEnvelope(t *testing.T) {
	t.Parallel()

	var got model.ListParams
	h := newMCPTestHandler(func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		got = params
		return &model.ListResult{Events: []model.ChangeEvent{
			{
				ID: "change1-end", ExternalID: "argo-end", UserName: "argocd@example.com", UserProvider: "beyond", UserSubject: "argocd@example.com",
				Timestamp: time.Date(2026, 10, 14, 20, 0, 0, 0, time.UTC), EventType: "deployment",
				Description: "api rollout finished", LongDescription: "revision abc123",
				Tags:      map[string]string{"service": "api", "change_id": "change1", "phase": "end"},
				Links:     []model.EventLink{{Label: "commit", URL: "https://github.com/example/api/commit/abc123"}},
				CreatedAt: time.Date(2026, 10, 14, 20, 0, 1, 0, time.UTC),
			},
			{
				ID: "change1-start", UserName: "argocd@example.com",
				Timestamp: time.Date(2026, 10, 14, 19, 45, 0, 0, time.UTC), EventType: "deployment",
				Description: "api rollout started",
			},
		}, TotalCount: 2, Limit: 25}, nil
	})

	text, isError := callListDeployments(t, h, map[string]any{
		"start_after":  "2026-10-14T11:30:00-07:00",
		"start_before": "2026-10-14T21:00:00Z",
		"service":      "api",
		"environment":  "production",
		"cluster":      "cluster-a",
		"offset":       0,
	})
	if isError {
		t.Fatalf("tools/call isError = true: %s", text)
	}

	want := `{"events":[` +
		`{"id":"change1-end","user_name":"argocd@example.com","timestamp":"2026-10-14T20:00:00Z","event_type":"deployment","description":"api rollout finished","long_description":"revision abc123","tags":{"change_id":"change1","phase":"end","service":"api"},"links":[{"url":"https://github.com/example/api/commit/abc123","label":"commit"}],"url":"https://changes.example.com/events/change1-end"},` +
		`{"id":"change1-start","user_name":"argocd@example.com","timestamp":"2026-10-14T19:45:00Z","event_type":"deployment","description":"api rollout started","long_description":"","url":"https://changes.example.com/events/change1-start"}` +
		`],"total_count":2,"offset":0,"coverage":"` + wantCoverage + `"}`
	if text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}

	wantStart := time.Date(2026, 10, 14, 18, 30, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC)
	if got.StartAfter == nil || !got.StartAfter.Equal(wantStart) || got.StartAfter.Location() != time.UTC {
		t.Errorf("StartAfter = %v, want %v in UTC", got.StartAfter, wantStart)
	}
	if got.StartBefore == nil || !got.StartBefore.Equal(wantEnd) {
		t.Errorf("StartBefore = %v, want %v", got.StartBefore, wantEnd)
	}
	if !got.TopLevel || got.Limit != 25 || got.Offset != 0 || got.Around != nil || got.EventType != "" || got.UserName != "" || got.AlertedOnly {
		t.Errorf("params = %+v, want top-level page of 25 at offset 0 with no other filters", got)
	}
	wantTags := map[string]string{"service": "api", "env": "production", "cluster": "cluster-a"}
	if !reflect.DeepEqual(got.Tags, wantTags) {
		t.Errorf("Tags = %v, want %v", got.Tags, wantTags)
	}
}

func TestMCPListDeploymentsOmitsUnsetFilters(t *testing.T) {
	t.Parallel()

	var got model.ListParams
	h := newMCPTestHandler(func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		got = params
		return &model.ListResult{Events: []model.ChangeEvent{}}, nil
	})
	arguments := windowArguments(0)
	arguments["environment"] = "production"
	page := listDeploymentsPage(t, h, arguments)

	if want := map[string]string{"env": "production"}; !reflect.DeepEqual(got.Tags, want) {
		t.Errorf("Tags = %v, want %v", got.Tags, want)
	}
	if events, ok := page["events"].([]any); !ok || len(events) != 0 {
		t.Errorf("events = %#v, want an empty array", page["events"])
	}
}

func TestMCPListDeploymentsDecodesLeniently(t *testing.T) {
	t.Parallel()

	var got model.ListParams
	h := newMCPTestHandler(func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		got = params
		return &model.ListResult{Events: []model.ChangeEvent{}}, nil
	})
	listDeploymentsPage(t, h, map[string]any{
		"start_after":  "2026-10-14T18:00:00Z",
		"start_before": "2026-10-14T21:00:00Z",
		"limit":        200,
		"type":         "star",
	})
	if got.Offset != 0 || got.Limit != 25 || got.EventType != "" || !got.TopLevel {
		t.Errorf("params = %+v, want omitted offset 0 and ignored unknown fields", got)
	}
}

func TestMCPListDeploymentsRejectsInvalidArguments(t *testing.T) {
	t.Parallel()

	const windowError = "PCR query requires an increasing window of at most 24h and offset 0-10000"
	tests := []struct {
		name      string
		arguments map[string]any
		want      string
	}{
		{"missing start_after", map[string]any{"start_before": "2026-10-14T21:00:00Z", "offset": 0}, "start_after must be RFC3339: "},
		{"natural-language start_after", map[string]any{"start_after": "yesterday", "start_before": "2026-10-14T21:00:00Z", "offset": 0}, "start_after must be RFC3339: "},
		{"date-only start_before", map[string]any{"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-15", "offset": 0}, "start_before must be RFC3339: "},
		{"empty window", map[string]any{"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-14T18:00:00Z", "offset": 0}, windowError},
		{"reversed window", map[string]any{"start_after": "2026-10-14T21:00:00Z", "start_before": "2026-10-14T18:00:00Z", "offset": 0}, windowError},
		{"window over 24 hours", map[string]any{"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-15T18:00:01Z", "offset": 0}, windowError},
		{"negative offset", windowArguments(-1), windowError},
		{"offset over 10000", windowArguments(10001), windowError},
		{"string offset", map[string]any{"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-14T21:00:00Z", "offset": "25"}, "invalid deployment arguments: "},
		{"fractional offset", map[string]any{"start_after": "2026-10-14T18:00:00Z", "start_before": "2026-10-14T21:00:00Z", "offset": 2.5}, "invalid deployment arguments: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			text, isError := callListDeployments(t, newMCPTestHandler(unexpectedList(t)), tc.arguments)
			if !isError {
				t.Fatalf("isError = false, want a tool error; text = %s", text)
			}
			if !strings.HasPrefix(text, tc.want) {
				t.Errorf("error = %q, want prefix %q", text, tc.want)
			}
		})
	}
}

func TestMCPListDeploymentsAcceptsBoundaryArguments(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	h := newMCPTestHandler(func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		calls.Add(1)
		return &model.ListResult{Events: []model.ChangeEvent{}, TotalCount: 0, Offset: params.Offset}, nil
	})
	page := listDeploymentsPage(t, h, map[string]any{
		"start_after":  "2026-10-14T18:00:00.5Z",
		"start_before": "2026-10-15T18:00:00.5Z",
		"offset":       10000,
	})
	if calls.Load() != 1 || page["offset"] != float64(10000) {
		t.Errorf("calls = %d, page = %v; want a 24-hour window at offset 10000 to be accepted", calls.Load(), page)
	}
}

func TestMCPListDeploymentsTruncatesOversizedText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		description     string
		longDescription string
		wantDescription string
		wantLong        string
		wantTruncated   bool
	}{
		{
			name:            "within limits",
			description:     strings.Repeat("d", 1000),
			longDescription: strings.Repeat("l", 4000),
			wantDescription: strings.Repeat("d", 1000),
			wantLong:        strings.Repeat("l", 4000),
		},
		{
			name:            "description over 1000 bytes keeps 500 runes",
			description:     strings.Repeat("d", 1001),
			longDescription: "short",
			wantDescription: strings.Repeat("d", 500),
			wantLong:        "short",
			wantTruncated:   true,
		},
		{
			name:            "long description over 4000 bytes keeps 2000 runes",
			description:     "short",
			longDescription: strings.Repeat("l", 4001),
			wantDescription: "short",
			wantLong:        strings.Repeat("l", 2000),
			wantTruncated:   true,
		},
		{
			// The thresholds count bytes and the cuts count runes.
			name:            "multibyte text is cut on rune boundaries",
			description:     strings.Repeat("界", 600),
			longDescription: strings.Repeat("é", 2500),
			wantDescription: strings.Repeat("界", 500),
			wantLong:        strings.Repeat("é", 2000),
			wantTruncated:   true,
		},
		{
			// 334 three-byte runes exceed 1000 bytes but not 500 runes, so the
			// flag is set although nothing is removed. This matches the
			// established behavior.
			name:            "byte threshold flags text shorter than the rune cut",
			description:     strings.Repeat("界", 334),
			longDescription: "short",
			wantDescription: strings.Repeat("界", 334),
			wantLong:        "short",
			wantTruncated:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newMCPTestHandler(func(context.Context, model.ListParams) (*model.ListResult, error) {
				return &model.ListResult{Events: []model.ChangeEvent{{
					ID: "evt", Description: tc.description, LongDescription: tc.longDescription,
				}}, TotalCount: 1}, nil
			})
			page := listDeploymentsPage(t, h, windowArguments(0))
			event := page["events"].([]any)[0].(map[string]any)
			if event["description"] != tc.wantDescription {
				t.Errorf("description has %d bytes, want %d", len(event["description"].(string)), len(tc.wantDescription))
			}
			if event["long_description"] != tc.wantLong {
				t.Errorf("long_description has %d bytes, want %d", len(event["long_description"].(string)), len(tc.wantLong))
			}
			truncated, present := event["description_truncated"]
			if tc.wantTruncated && truncated != true {
				t.Errorf("description_truncated = %v, want true", truncated)
			}
			if !tc.wantTruncated && present {
				t.Errorf("description_truncated present (%v), want omitted", truncated)
			}
		})
	}
}

func TestMCPListDeploymentsPaginates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		offset     int
		returned   int
		total      int
		wantNext   any
		wantErrMsg string
	}{
		{name: "first page of several", offset: 0, returned: 25, total: 60, wantNext: float64(25)},
		{name: "middle page", offset: 25, returned: 25, total: 60, wantNext: float64(50)},
		{name: "last page", offset: 50, returned: 10, total: 60},
		{name: "empty window", offset: 0, returned: 0, total: 0},
		{name: "offset past the end", offset: 100, returned: 0, total: 30},
		{name: "rows vanished during read", offset: 25, returned: 0, total: 30, wantErrMsg: "PCR pagination changed during read; retry the fixed window from offset 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newMCPTestHandler(func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
				if params.Offset != tc.offset || params.Limit != 25 {
					t.Errorf("offset/limit = %d/%d, want %d/25", params.Offset, params.Limit, tc.offset)
				}
				events := make([]model.ChangeEvent, tc.returned)
				for i := range events {
					events[i] = model.ChangeEvent{ID: "evt"}
				}
				return &model.ListResult{Events: events, TotalCount: tc.total, Limit: 25, Offset: params.Offset}, nil
			})
			if tc.wantErrMsg != "" {
				text, isError := callListDeployments(t, h, windowArguments(tc.offset))
				if !isError || text != tc.wantErrMsg {
					t.Errorf("result = %q (isError %v), want error %q", text, isError, tc.wantErrMsg)
				}
				return
			}
			page := listDeploymentsPage(t, h, windowArguments(tc.offset))
			if page["total_count"] != float64(tc.total) || page["offset"] != float64(tc.offset) {
				t.Errorf("total_count/offset = %v/%v, want %d/%d", page["total_count"], page["offset"], tc.total, tc.offset)
			}
			if page["next_offset"] != tc.wantNext {
				t.Errorf("next_offset = %v, want %v", page["next_offset"], tc.wantNext)
			}
			if events, ok := page["events"].([]any); !ok || len(events) != tc.returned {
				t.Errorf("events = %#v, want %d events", page["events"], tc.returned)
			}
		})
	}
}

func TestMCPListDeploymentsBuildsEscapedPermalinks(t *testing.T) {
	t.Parallel()

	h := newMCPTestHandler(func(context.Context, model.ListParams) (*model.ListResult, error) {
		return &model.ListResult{Events: []model.ChangeEvent{{ID: "evt/1 ?#"}}, TotalCount: 1}, nil
	})
	page := listDeploymentsPage(t, h, windowArguments(0))
	event := page["events"].([]any)[0].(map[string]any)
	if want := "https://changes.example.com/events/evt%2F1%20%3F%23"; event["url"] != want {
		t.Errorf("url = %v, want %s", event["url"], want)
	}
}

func TestMCPListDeploymentsHidesStoreErrors(t *testing.T) {
	t.Parallel()

	h := newMCPTestHandler(func(context.Context, model.ListParams) (*model.ListResult, error) {
		return nil, errors.New("dial tcp 10.0.0.5:5432: password authentication failed for user pcr")
	})
	text, isError := callListDeployments(t, h, windowArguments(0))
	if !isError || text != "PCR list events: an internal error occurred" {
		t.Errorf("result = %q (isError %v), want a generic tool error", text, isError)
	}
}
