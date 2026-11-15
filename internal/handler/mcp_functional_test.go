//go:build integration

package handler_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

// TestMCPListDeploymentsMatchesRESTClientMapping checks list_deployments
// against real PostgreSQL by comparing its text, byte for byte, with what a
// REST client of GET /api/v1/events produces for the same window, filters,
// and offset.
func TestMCPListDeploymentsMatchesRESTClientMapping(t *testing.T) {
	t.Parallel()

	dashboard := seededDashboardRouter(t)
	now := time.Now().UTC()
	// The shared fixture is seeded relative to now (within the last few hours
	// and at -14 days); a window a week back overlaps neither.
	base := now.Add(-7 * 24 * time.Hour).Truncate(time.Hour)
	seedDeploymentWindow(t, dashboard, base)

	tests := []struct {
		name                       string
		start, end                 time.Time
		service, environment, clus string
		offset                     int
		wantEvents                 int
		wantNext                   bool
	}{
		{name: "first page", start: base, end: base.Add(3 * time.Hour), wantEvents: 25, wantNext: true},
		{name: "second page", start: base, end: base.Add(3 * time.Hour), offset: 25, wantEvents: 9},
		{name: "all filters", start: base, end: base.Add(3 * time.Hour), service: "api", environment: "production", clus: "cluster-a", wantEvents: 13},
		{name: "one filter", start: base, end: base.Add(3 * time.Hour), service: "web", wantEvents: 20},
		{name: "colon in tag value", start: base, end: base.Add(3 * time.Hour), clus: "edge:west", wantEvents: 1},
		{name: "end bound is exclusive", start: base.Add(-time.Hour), end: base, wantEvents: 1},
		// Nine of the shared fixture's top-level events fall in its last day.
		{name: "fixture data in the last day", start: now.Add(time.Minute - 24*time.Hour), end: now.Add(time.Minute), wantEvents: 9},
		{name: "empty window", start: base.Add(-48 * time.Hour), end: base.Add(-47 * time.Hour)},
		{name: "offset past the end", start: base, end: base.Add(3 * time.Hour), offset: 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			arguments := map[string]any{
				"start_after":  tc.start.Format(time.RFC3339Nano),
				"start_before": tc.end.Format(time.RFC3339Nano),
				"offset":       tc.offset,
			}
			for key, value := range map[string]string{"service": tc.service, "environment": tc.environment, "cluster": tc.clus} {
				if value != "" {
					arguments[key] = value
				}
			}
			got := mcpListDeploymentsText(t, dashboard, arguments)
			want := restClientListDeployments(t, dashboard, tc.start, tc.end, tc.service, tc.environment, tc.clus, tc.offset)
			if got != want {
				t.Fatalf("MCP text differs from the REST client mapping\nMCP:  %s\nREST: %s", got, want)
			}

			var page struct {
				Events     []map[string]any `json:"events"`
				NextOffset *int             `json:"next_offset"`
			}
			if err := json.Unmarshal([]byte(got), &page); err != nil {
				t.Fatalf("decode page: %v", err)
			}
			if len(page.Events) != tc.wantEvents {
				t.Errorf("events = %d, want %d", len(page.Events), tc.wantEvents)
			}
			if (page.NextOffset != nil) != tc.wantNext {
				t.Errorf("next_offset = %v, want present=%v", page.NextOffset, tc.wantNext)
			}
		})
	}
}

// seedDeploymentWindow records 34 top-level events in [base, base+3h), a meta
// event inside it, and one event on each side of the window.
func seedDeploymentWindow(t *testing.T, dashboard *seededDashboard, base time.Time) {
	t.Helper()
	create := func(request model.CreateChangeRequest) string {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("encode event: %v", err)
		}
		response := performAPIRequest(t, dashboard, http.MethodPost, "/api/v1/events", string(body))
		if response.Code != http.StatusCreated {
			t.Fatalf("create event status = %d; body = %s", response.Code, response.Body.String())
		}
		var event model.ChangeEvent
		if err := json.Unmarshal(response.Body.Bytes(), &event); err != nil {
			t.Fatalf("decode created event: %v", err)
		}
		return event.ID
	}
	at := func(offset time.Duration) *time.Time {
		timestamp := base.Add(offset)
		return &timestamp
	}

	var firstID string
	for i := range 32 {
		tags := map[string]string{"service": "web", "env": "production", "cluster": "cluster-b"}
		if i%8 < 3 {
			tags = map[string]string{"service": "api", "env": "production", "cluster": "cluster-a"}
		}
		// Pairs of Argo-style start/end events share a correlation ID.
		tags["change_id"] = "argocd:change-" + strconv.Itoa(i/2)
		tags["phase"] = "start"
		if i%2 == 1 {
			tags["phase"] = "end"
		}
		id := create(model.CreateChangeRequest{
			UserName: "argocd@example.com", EventType: model.EventTypeDeployment, Timestamp: at(time.Duration(i) * 5 * time.Minute),
			Description: fmt.Sprintf("Argo deploy %d", i), LongDescription: `{"revision": "abc` + strconv.Itoa(i) + `"}`,
			Links: []model.EventLink{{Label: "Argo application", URL: "https://argo.example.com/applications/app-" + strconv.Itoa(i)}, {URL: "https://github.com/example/deploy/commit/abc"}},
			Tags:  tags,
		})
		if firstID == "" {
			firstID = id
		}
	}
	create(model.CreateChangeRequest{
		UserName: "flag-bot", EventType: model.EventTypeFeatureFlag, Timestamp: at(170 * time.Minute),
		Description:     strings.Repeat("界", 600),
		LongDescription: strings.Repeat("long ", 1000),
		Tags:            map[string]string{"service": "api", "env": "production", "cluster": "cluster-a"},
	})
	create(model.CreateChangeRequest{
		UserName: "edge-bot", EventType: model.EventTypeK8sChange, Timestamp: at(175 * time.Minute),
		Description: "edge config <b>&amp;</b>", Tags: map[string]string{"cluster": "edge:west"},
	})
	create(model.CreateChangeRequest{ParentID: firstID, UserName: "on-call", EventType: model.EventTypeStar, Timestamp: at(time.Minute), Description: "starred"})
	create(model.CreateChangeRequest{UserName: "early", EventType: "deployment", Timestamp: at(-time.Minute), Description: "before the window"})
	create(model.CreateChangeRequest{UserName: "late", EventType: "deployment", Timestamp: at(3 * time.Hour), Description: "at the exclusive end"})
}

func mcpListDeploymentsText(t *testing.T, dashboard *seededDashboard, arguments map[string]any) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "list_deployments", "arguments": arguments}})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer demo-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()
	dashboard.ServeHTTP(response, request)
	var decoded struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || response.Code != http.StatusOK {
		t.Fatalf("POST /mcp status = %d (%v); body = %s", response.Code, err, response.Body.String())
	}
	if decoded.Result.IsError || len(decoded.Result.Content) != 1 {
		t.Fatalf("tools/call result = %s", response.Body.String())
	}
	return decoded.Result.Content[0].Text
}

// The types and mapping below are a reference REST client that reads
// GET /api/v1/events and reshapes it for models, as list_deployments must.
// Only the HTTP transport is replaced by the in-process router.
type restClientEvent struct {
	ID              string            `json:"id"`
	UserName        string            `json:"user_name"`
	Timestamp       time.Time         `json:"timestamp"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Tags            map[string]string `json:"tags,omitempty"`
	Links           []restClientLink  `json:"links,omitempty"`
	URL             string            `json:"url"`
	Truncated       bool              `json:"description_truncated,omitempty"`
}

type restClientLink struct {
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

type restClientPage struct {
	Events     []restClientEvent `json:"events"`
	TotalCount int               `json:"total_count"`
	Offset     int               `json:"offset"`
	NextOffset *int              `json:"next_offset,omitempty"`
	Coverage   string            `json:"coverage"`
}

func restClientListDeployments(t *testing.T, h http.Handler, start, end time.Time, service, environment, cluster string, offset int) string {
	t.Helper()
	query := url.Values{
		"start_after": {start.UTC().Format(time.RFC3339Nano)}, "start_before": {end.UTC().Format(time.RFC3339Nano)},
		"top_level": {"true"}, "limit": {"25"}, "offset": {strconv.Itoa(offset)},
	}
	for key, value := range map[string]string{"service": service, "env": environment, "cluster": cluster} {
		if value != "" {
			query.Add("tag", key+":"+value)
		}
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?"+query.Encode(), nil)
	request.Header.Set("Authorization", "Bearer demo-token")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/events status = %d; body = %s", response.Code, response.Body.String())
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		t.Fatalf("read REST body: %v (%d bytes)", err, len(body))
	}
	var raw struct {
		Events     []restClientEvent `json:"events"`
		TotalCount *int              `json:"total_count"`
		Offset     int               `json:"offset"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode REST events: %v", err)
	}
	if raw.TotalCount == nil || *raw.TotalCount < 0 || raw.Offset != offset || len(raw.Events) > 25 {
		t.Fatalf("REST returned an invalid pagination envelope: %s", body)
	}
	if len(raw.Events) == 0 && offset < *raw.TotalCount {
		t.Fatalf("REST pagination changed during read")
	}
	page := restClientPage{Events: raw.Events, TotalCount: *raw.TotalCount, Offset: offset,
		Coverage: "PCR reporting is opt-in and may miss deliveries; absence of recorded changes does not prove no deployment. Offset pagination is not a snapshot: deduplicate event IDs and recheck if counts change."}
	for index := range page.Events {
		event := &page.Events[index]
		event.URL = functionalPublicURL + "/events/" + url.PathEscape(event.ID)
		if len(event.Description) > 1000 {
			event.Description = string([]rune(event.Description)[:min(500, len([]rune(event.Description)))])
			event.Truncated = true
		}
		if len(event.LongDescription) > 4000 {
			event.LongDescription = string([]rune(event.LongDescription)[:min(2000, len([]rune(event.LongDescription)))])
			event.Truncated = true
		}
	}
	next := offset + len(raw.Events)
	if next < page.TotalCount {
		page.NextOffset = &next
	}
	text, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("encode REST client page: %v", err)
	}
	return string(text)
}
