package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// testCase is a single named smoke check. Run gets a context-aware client
// and a fixture struct it can mutate to share state with later cases (e.g.
// a parent event ID created by an earlier case).
type testCase struct {
	Name string
	Run  func(ctx context.Context, c *client, f *fixture) error
}

// fixture carries state between cases that depend on each other (created
// IDs, login session). It is reset to a zero value at the start of each
// run so cases are deterministic across invocations.
type fixture struct {
	parentEventID string
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
		{"toggle_star_via_api", caseToggleStarAPI},
		{"annotations_show_starred", caseAnnotationsStarred},
		{"toggle_star_again_unstars", caseToggleStarTwice},
		{"create_alert_meta_event", caseCreateAlert},
		{"list_filter_alerted_only", caseListFilterAlerted},
		{"login_form_renders", caseLoginFormRenders},
		{"login_post_sets_session_cookie", caseLoginPostCookie},
		{"login_oversized_body_returns_413", caseLoginOversizedBody},
		{"dashboard_with_session_cookie_renders", caseDashboardWithCookie},
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

// --- login flow + dashboard + CSRF / size limit ---

func caseLoginFormRenders(ctx context.Context, c *client, _ *fixture) error {
	r, err := c.do(ctx, http.MethodGet, "/login", nil, withAuth(authNone))
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	if !strings.Contains(string(r.Body), `name="token"`) {
		return fmt.Errorf("login form missing token input")
	}
	return nil
}

func caseLoginPostCookie(ctx context.Context, c *client, _ *fixture) error {
	form := url.Values{"token": {c.token}}
	r, err := c.postForm(ctx, "/login", form, withAuth(authNone))
	if err != nil {
		return err
	}
	// Login redirects to / on success; the http.Client default follows the
	// redirect, so we'll see the dashboard 200 here. Either way, the cookie
	// must have been set on the jar.
	if r.Status != http.StatusOK && r.Status != http.StatusSeeOther {
		return fmt.Errorf("expected 200 or 303 after login, got %d (body: %s)", r.Status, truncate(r.Body, 200))
	}
	if !sessionCookieSet(c) {
		return fmt.Errorf("session cookie not set after login")
	}
	return nil
}

func caseLoginOversizedBody(ctx context.Context, c *client, _ *fixture) error {
	// 16 KiB body (well above the 8 KiB cap in parseBoundedPostForm).
	huge := "token=" + strings.Repeat("a", 16<<10)
	r, err := c.do(ctx, http.MethodPost, "/login",
		strings.NewReader(huge),
		withAuth(authNone),
		withContentType("application/x-www-form-urlencoded"),
	)
	if err != nil {
		return err
	}
	return expectStatus(r, http.StatusRequestEntityTooLarge)
}

func caseDashboardWithCookie(ctx context.Context, c *client, _ *fixture) error {
	if !sessionCookieSet(c) {
		return fmt.Errorf("skipped: no session cookie (login must run first)")
	}
	// authNone -> rely on the cookie jar. The Auth middleware accepts
	// session cookies even without a Bearer header.
	r, err := c.do(ctx, http.MethodGet, "/", nil, withAuth(authNone))
	if err != nil {
		return err
	}
	if err := expectStatus(r, http.StatusOK); err != nil {
		return err
	}
	body := string(r.Body)
	if !strings.Contains(body, "<html") {
		return fmt.Errorf("dashboard response does not look like HTML (first 200 bytes: %s)", truncate(r.Body, 200))
	}
	// Sanity check: dashboard renders a CSRF token for star forms.
	if !csrfTokenPresent(body) {
		return fmt.Errorf("dashboard HTML missing csrf_token input")
	}
	return nil
}

// --- helpers ---

var csrfInputRE = regexp.MustCompile(`name="csrf_token"\s+value="[^"]+"`)

func csrfTokenPresent(html string) bool {
	return csrfInputRE.MatchString(html)
}

func sessionCookieSet(c *client) bool {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return false
	}
	for _, cookie := range c.http.Jar.Cookies(u) {
		if cookie.Name == "pcr_session" && cookie.Value != "" {
			return true
		}
	}
	return false
}
