package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

// testCase is a single named smoke check. Run gets a context-aware client
// and a fixture struct it can mutate to share state with later cases (e.g.
// a parent event ID created by an earlier case).
type testCase struct {
	Name string
	Run  func(ctx context.Context, c *client, f *fixture) error
}

// fixture carries state between cases that depend on each other (created
// IDs). It is reset to a zero value at the start of each
// run so cases are deterministic across invocations.
type fixture struct {
	parentEventID      string
	currentActiveID    string
	currentCompletedID string
	currentSiteID      string
	currentOtherTeamID string
}

// allCases is executed in order. Earlier cases that fail do not abort the
// run -- subsequent independent cases still execute. Cases that depend on
// fixture state should check for it and skip / fail informatively.
func allCases() []testCase {
	return []testCase{
		{"health_no_auth_required", caseHealthNoAuth},
		{"health_with_auth", caseHealthWithAuth},
		{"api_rejects_no_auth", caseAPINoAuth},
		{"api_rejects_wrong_token", caseAPIWrongToken},
		{"create_event", caseCreateEvent},
		{"create_idempotent_external_id", caseCreateIdempotent},
		{"get_event_by_id", caseGetEvent},
		{"create_validation_missing_user_name", caseCreateValidationUser},
		{"create_validation_missing_event_type", caseCreateValidationType},
		{"list_filter_user", caseListFilterUser},
		{"list_filter_type", caseListFilterType},
		{"list_filter_tag", caseListFilterTag},
		{"create_current_fixtures", caseCreateCurrentFixtures},
		{"list_current_for_team", caseListCurrentForTeam},
		{"toggle_star_via_api", caseToggleStarAPI},
		{"annotations_show_starred", caseAnnotationsStarred},
		{"toggle_star_again_unstars", caseToggleStarTwice},
		{"create_alert_meta_event", caseCreateAlert},
		{"list_filter_alerted_only", caseListFilterAlerted},
		{"mcp_rejects_no_auth", caseMCPNoAuth},
		{"mcp_lists_read_only_tool", caseMCPToolsList},
		{"mcp_list_deployments", caseMCPListDeployments},
		{"login_form_renders", caseLoginFormRenders},
	}
}

// --- health & auth gating ---

func caseHealthNoAuth(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/api/v1/health", nil, withAuth(authNone))
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	return nil
}

func caseHealthWithAuth(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusOK)
}

func caseAPINoAuth(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/api/v1/events", nil, withAuth(authNone))
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusUnauthorized)
}

func caseAPIWrongToken(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/api/v1/events", nil, withAuth(authWrongBearer))
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusUnauthorized)
}

// --- create / read ---

func caseCreateEvent(ctx context.Context, c *client, f *fixture) error {
	body := model.CreateChangeRequest{
		UserName:        "smoke",
		EventType:       model.EventTypeDeployment,
		ExternalID:      "smoke-deploy-1",
		Description:     "deploy v1.3.0 to prod",
		LongDescription: "Includes feature X and Y",
		Tags:            map[string]string{"env": "prod", "service": "api", "version": "1.3.0"},
	}
	var got model.ChangeEvent
	r, err := c.postJSON(ctx, "/api/v1/events", body, &got)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusCreated); err != nil {
		return err
	}
	if got.ID == "" {
		return fmt.Errorf("expected non-empty event id, got empty")
	}
	if got.UserName != "smoke" || got.EventType != model.EventTypeDeployment {
		return fmt.Errorf("response fields not echoed: %+v", got)
	}
	if got.Tags["env"] != "prod" {
		return fmt.Errorf("tags not persisted: %+v", got.Tags)
	}
	if loc := r.Header.Get("Location"); loc != "/api/v1/events/"+got.ID {
		return fmt.Errorf("location header = %q, want /api/v1/events/%s", loc, got.ID)
	}
	f.parentEventID = got.ID
	return nil
}

func caseCreateIdempotent(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	body := model.CreateChangeRequest{
		UserName:    "smoke",
		EventType:   model.EventTypeDeployment,
		ExternalID:  "smoke-deploy-1", // same as caseCreateEvent
		Description: "this should not appear -- idempotent dedup",
	}
	var got model.ChangeEvent
	r, err := c.postJSON(ctx, "/api/v1/events", body, &got)
	if err != nil {
		return err
	}
	// Idempotent re-create returns the existing event with 200 (not 201).
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if got.ID != f.parentEventID {
		return fmt.Errorf("expected existing id %s, got %s", f.parentEventID, got.ID)
	}
	if got.Description != "deploy v1.3.0 to prod" {
		return fmt.Errorf("expected original description preserved, got %q", got.Description)
	}
	return nil
}

func caseGetEvent(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	var got model.ChangeEvent
	r, err := c.getJSON(ctx, "/api/v1/events/"+f.parentEventID, &got)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if got.ID != f.parentEventID {
		return fmt.Errorf("returned id %s != requested %s", got.ID, f.parentEventID)
	}
	return nil
}

func caseCreateValidationUser(ctx context.Context, c *client, _ *fixture) error {
	body := model.CreateChangeRequest{
		EventType:   model.EventTypeDeployment,
		Description: "missing user_name",
	}
	r, err := c.postJSON(ctx, "/api/v1/events", body, nil)
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusBadRequest)
}

func caseCreateValidationType(ctx context.Context, c *client, _ *fixture) error {
	body := model.CreateChangeRequest{
		UserName:    "smoke",
		Description: "missing event_type",
	}
	r, err := c.postJSON(ctx, "/api/v1/events", body, nil)
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusBadRequest)
}

// --- list filters ---

func caseListFilterUser(ctx context.Context, c *client, _ *fixture) error {
	var lr model.ListResult
	r, err := c.getJSON(ctx, "/api/v1/events?user=smoke", &lr)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if lr.TotalCount == 0 {
		return fmt.Errorf("expected at least one event for user=smoke, got 0")
	}
	for _, e := range lr.Events {
		if e.UserName != "smoke" {
			return fmt.Errorf("filter leak: got event with user_name=%q", e.UserName)
		}
	}
	return nil
}

func caseListFilterType(ctx context.Context, c *client, _ *fixture) error {
	var lr model.ListResult
	r, err := c.getJSON(ctx, "/api/v1/events?type="+model.EventTypeDeployment, &lr)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	for _, e := range lr.Events {
		if e.EventType != model.EventTypeDeployment {
			return fmt.Errorf("filter leak: got event with type=%q", e.EventType)
		}
	}
	return nil
}

func caseListFilterTag(ctx context.Context, c *client, _ *fixture) error {
	var lr model.ListResult
	r, err := c.getJSON(ctx, "/api/v1/events?tag=env:prod", &lr)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if lr.TotalCount == 0 {
		return fmt.Errorf("expected at least one event with env:prod tag, got 0")
	}
	for _, e := range lr.Events {
		if e.Tags["env"] != "prod" {
			return fmt.Errorf("filter leak: event %s has tags=%v", e.ID, e.Tags)
		}
	}
	return nil
}

func caseCreateCurrentFixtures(ctx context.Context, c *client, f *fixture) error {
	fixtures := []struct {
		request model.CreateChangeRequest
		assign  func(string)
	}{
		{
			request: model.CreateChangeRequest{
				UserName:    "smoke",
				EventType:   model.EventTypeDeployment,
				ExternalID:  "smoke-current-active-start",
				Description: "active payments deployment",
				Tags:        map[string]string{"deploy_id": "smoke-active", "phase": "start", "team": "payments", "scope": "service", "severity": "sev2"},
			},
			assign: func(id string) { f.currentActiveID = id },
		},
		{
			request: model.CreateChangeRequest{
				UserName:    "smoke",
				EventType:   model.EventTypeDeployment,
				ExternalID:  "smoke-current-completed-start",
				Description: "completed payments deployment",
				Tags:        map[string]string{"deploy_id": "smoke-completed", "phase": "start", "team": "payments", "scope": "service", "severity": "sev2"},
			},
			assign: func(id string) { f.currentCompletedID = id },
		},
		{
			request: model.CreateChangeRequest{
				UserName:    "smoke",
				EventType:   model.EventTypeDeployment,
				ExternalID:  "smoke-current-completed-end",
				Description: "completed payments deployment finished",
				Tags:        map[string]string{"deploy_id": "smoke-completed", "phase": "end"},
			},
		},
		{
			request: model.CreateChangeRequest{
				UserName:    "smoke",
				EventType:   "incident",
				ExternalID:  "smoke-current-site-start",
				Description: "active site incident",
				Tags:        map[string]string{"change_id": "smoke-site", "phase": "start", "team": "platform", "scope": "site", "severity": "SEV0"},
			},
			assign: func(id string) { f.currentSiteID = id },
		},
		{
			request: model.CreateChangeRequest{
				UserName:    "smoke",
				EventType:   model.EventTypeDeployment,
				ExternalID:  "smoke-current-other-team-start",
				Description: "active platform deployment",
				Tags:        map[string]string{"change_id": "smoke-other-team", "phase": "start", "team": "platform", "scope": "service", "severity": "sev1"},
			},
			assign: func(id string) { f.currentOtherTeamID = id },
		},
	}

	for _, item := range fixtures {
		var event model.ChangeEvent
		response, err := c.postJSON(ctx, "/api/v1/events", item.request, &event)
		if err != nil {
			return err
		}
		if response.Status != http.StatusCreated && response.Status != http.StatusOK {
			return expectStatus(response, http.StatusCreated)
		}
		if item.assign != nil {
			item.assign(event.ID)
		}
	}
	return nil
}

func caseListCurrentForTeam(ctx context.Context, c *client, f *fixture) error {
	if f.currentActiveID == "" || f.currentCompletedID == "" || f.currentSiteID == "" || f.currentOtherTeamID == "" {
		return fmt.Errorf("skipped: current fixtures were not created")
	}

	var result model.ListResult
	response, err := c.getJSON(ctx, "/api/v1/current?for_team=payments", &result)
	if err != nil {
		return err
	}
	if err := expectStatus(response, http.StatusOK); err != nil {
		return err
	}

	found := make(map[string]bool)
	for _, event := range result.Events {
		found[event.ID] = true
	}
	if !found[f.currentActiveID] {
		return fmt.Errorf("active team deployment %s missing from Current", f.currentActiveID)
	}
	if !found[f.currentSiteID] {
		return fmt.Errorf("site-wide incident %s missing from Current", f.currentSiteID)
	}
	if found[f.currentCompletedID] {
		return fmt.Errorf("completed deployment %s present in Current", f.currentCompletedID)
	}
	if found[f.currentOtherTeamID] {
		return fmt.Errorf("unrelated team deployment %s present in Current", f.currentOtherTeamID)
	}
	return nil
}

// --- star / annotations ---

func caseToggleStarAPI(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	r, err := c.do(ctx, http.MethodPost, "/api/v1/events/"+f.parentEventID+"/star", nil)
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusCreated)
}

func caseAnnotationsStarred(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	var ann model.EventAnnotations
	r, err := c.getJSON(ctx, "/api/v1/events/"+f.parentEventID+"/annotations", &ann)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if !ann.Starred {
		return fmt.Errorf("expected starred=true after toggle, got %+v", ann)
	}
	return nil
}

func caseToggleStarTwice(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	r, err := c.do(ctx, http.MethodPost, "/api/v1/events/"+f.parentEventID+"/star", nil)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusCreated); err != nil {
		return err
	}
	var ann model.EventAnnotations
	if _, err := c.getJSON(ctx, "/api/v1/events/"+f.parentEventID+"/annotations", &ann); err != nil {
		return err
	}
	if ann.Starred {
		return fmt.Errorf("expected starred=false after second toggle, got %+v", ann)
	}
	return nil
}

// --- alert meta-event ---

func caseCreateAlert(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	body := model.CreateChangeRequest{
		ParentID:    f.parentEventID,
		UserName:    "smoke",
		EventType:   model.EventTypeAlert,
		Description: "smoke triggered alert",
	}
	var got model.ChangeEvent
	r, err := c.postJSON(ctx, "/api/v1/events", body, &got)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusCreated); err != nil {
		return err
	}
	if got.ParentID != f.parentEventID {
		return fmt.Errorf("alert ParentID = %q, want %q", got.ParentID, f.parentEventID)
	}
	return nil
}

func caseListFilterAlerted(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	var lr model.ListResult
	r, err := c.getJSON(ctx, "/api/v1/events?alerted=true&top_level=true", &lr)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	found := false
	for _, e := range lr.Events {
		if e.ID == f.parentEventID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("expected parent event %s in alerted list, not found among %d results", f.parentEventID, lr.TotalCount)
	}
	return nil
}

// --- MCP ---

// mcpPost sends one JSON-RPC request to /mcp and decodes the result on 200.
func mcpPost(ctx context.Context, c *client, method string, params, result any, opts ...reqOpt) (*response, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", method, err)
	}
	opts = append(opts, withContentType("application/json"), withAccept("application/json, text/event-stream"))
	r, err := c.do(ctx, http.MethodPost, "/mcp", bytes.NewReader(body), opts...)
	if err != nil || r.Status != http.StatusOK || result == nil {
		return r, err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(r.Body, &envelope); err != nil {
		return r, fmt.Errorf("decode %s: %w (body: %s)", method, err, truncate(r.Body, 200))
	}
	if envelope.Error != nil {
		return r, fmt.Errorf("%s JSON-RPC error: %s", method, envelope.Error)
	}
	return r, json.Unmarshal(envelope.Result, result)
}

func caseMCPNoAuth(ctx context.Context, c *client, _ *fixture) error {
	r, err := mcpPost(ctx, c, "tools/list", map[string]any{}, nil, withAuth(authNone))
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusUnauthorized)
}

func caseMCPToolsList(ctx context.Context, c *client, _ *fixture) error {
	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			Annotations struct {
				ReadOnlyHint    bool  `json:"readOnlyHint"`
				DestructiveHint *bool `json:"destructiveHint"`
			} `json:"annotations"`
		} `json:"tools"`
	}
	r, err := mcpPost(ctx, c, "tools/list", map[string]any{}, &listed)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if len(listed.Tools) != 1 || listed.Tools[0].Name != "list_deployments" {
		return fmt.Errorf("tools = %+v, want only list_deployments", listed.Tools)
	}
	if hints := listed.Tools[0].Annotations; !hints.ReadOnlyHint || hints.DestructiveHint == nil || *hints.DestructiveHint {
		return fmt.Errorf("annotations = %+v, want read-only and non-destructive", hints)
	}
	return nil
}

func caseMCPListDeployments(ctx context.Context, c *client, f *fixture) error {
	if f.parentEventID == "" {
		return fmt.Errorf("skipped: parent event was not created")
	}
	now := time.Now().UTC()
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	r, err := mcpPost(ctx, c, "tools/call", map[string]any{"name": "list_deployments", "arguments": map[string]any{
		"start_after":  now.Add(-time.Hour).Format(time.RFC3339),
		"start_before": now.Add(time.Minute).Format(time.RFC3339),
		"service":      "api",
		"environment":  "prod",
		"offset":       0,
	}}, &result)
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if result.IsError || len(result.Content) != 1 {
		return fmt.Errorf("tools/call result = %+v, want one non-error text block", result)
	}
	var page struct {
		Events []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"events"`
		TotalCount int    `json:"total_count"`
		Coverage   string `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &page); err != nil {
		return fmt.Errorf("decode list_deployments page: %w", err)
	}
	for _, event := range page.Events {
		if event.ID == f.parentEventID {
			if !strings.HasSuffix(event.URL, "/events/"+f.parentEventID) {
				return fmt.Errorf("permalink = %q, want a dashboard event URL", event.URL)
			}
			return nil
		}
	}
	return fmt.Errorf("event %s missing from %d list_deployments results", f.parentEventID, page.TotalCount)
}

// --- login flow + dashboard + CSRF / size limit ---

func caseLoginFormRenders(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/login", nil, withAuth(authNone))
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if !strings.Contains(string(r.Body), "Sign in with GitHub") {
		return fmt.Errorf("login form missing configured provider button")
	}
	return nil
}
