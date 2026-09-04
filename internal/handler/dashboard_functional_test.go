//go:build integration

package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sarahmaeve/go-prod-change-registry/internal/config"
	"github.com/sarahmaeve/go-prod-change-registry/internal/fixture"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	postgresdb "github.com/sarahmaeve/go-prod-change-registry/internal/postgres"
	"github.com/sarahmaeve/go-prod-change-registry/internal/router"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	postgresstore "github.com/sarahmaeve/go-prod-change-registry/internal/store/postgres"
)

func TestSeededDashboardViews(t *testing.T) {
	t.Parallel()

	r := seededDashboardRouter(t)

	current := renderDashboard(t, r, "/?view=current&team=payments")
	for _, want := range []string{
		`data-interface="phosphor-deck"`,
		`class="dashboard-summary instrument-housing"`,
		"Active high-visibility changes",
		"Ledger reconciliation backfill",
		"Primary card vault key rotation",
		"West coast edge traffic evacuation",
		"Orders archive compaction",
	} {
		if !strings.Contains(current, want) {
			t.Errorf("Current response does not contain %q", want)
		}
	}

	filteredSite := renderDashboard(t, r, "/?view=current&team=payments&type=incident&scope=site&severity=sev0")
	filteredSiteTable := dashboardTableBody(t, filteredSite)
	filteredSiteBanner := dashboardBannerBody(t, filteredSite)
	if !strings.Contains(filteredSiteTable, "West coast edge traffic evacuation") || !strings.Contains(filteredSiteBanner, "West coast edge traffic evacuation") {
		t.Error("site/SEV0 filters do not retain the site incident in both the Current table and high-visibility banner")
	}
	for _, excluded := range []string{"Primary card vault key rotation", "Orders archive compaction", "Ingress controller fleet replacement"} {
		if strings.Contains(filteredSiteTable, excluded) || strings.Contains(filteredSiteBanner, excluded) {
			t.Errorf("site/SEV0 filters unexpectedly retain %q", excluded)
		}
	}

	filteredMaintenance := renderDashboard(t, r, "/?view=current&team=payments&type=maintenance&severity=sev1")
	filteredMaintenanceTable := dashboardTableBody(t, filteredMaintenance)
	filteredMaintenanceBanner := dashboardBannerBody(t, filteredMaintenance)
	for _, want := range []string{"Primary card vault key rotation", "Orders archive compaction"} {
		if !strings.Contains(filteredMaintenanceTable, want) || !strings.Contains(filteredMaintenanceBanner, want) {
			t.Errorf("maintenance/SEV1 filters do not retain %q in both the Current table and high-visibility banner", want)
		}
	}
	for _, excluded := range []string{"West coast edge traffic evacuation", "Ingress controller fleet replacement", "Ledger reconciliation backfill"} {
		if strings.Contains(filteredMaintenanceTable, excluded) || strings.Contains(filteredMaintenanceBanner, excluded) {
			t.Errorf("maintenance/SEV1 filters unexpectedly retain %q", excluded)
		}
	}
	for _, unwanted := range []string{
		"Production signal deck",
		"Change operations",
		"Immutable production telemetry",
		"Registry online",
		"SYS/01",
		"Live telemetry",
	} {
		if strings.Contains(current, unwanted) {
			t.Errorf("Current response contains decorative copy %q", unwanted)
		}
	}
	currentTable := dashboardTableBody(t, current)
	for _, excluded := range []string{
		"Checkout API 2026.08.24.3 rollout",
		"Ingress controller fleet replacement",
		"Primary card vault key rotation (redelivery)",
		"Legacy event without a logical operation ID",
	} {
		if strings.Contains(currentTable, excluded) {
			t.Errorf("Current table unexpectedly contains %q", excluded)
		}
	}

	apiRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current?for_team=payments", nil)
	apiResponse := httptest.NewRecorder()
	r.ServeHTTP(apiResponse, apiRequest)
	var currentResult model.ListResult
	if err := json.NewDecoder(apiResponse.Body).Decode(&currentResult); err != nil {
		t.Fatalf("decode seeded Current API response: %v", err)
	}
	if apiResponse.Code != http.StatusOK || currentResult.TotalCount != 4 {
		t.Errorf("seeded Current API = status %d, total %d; want 200, 4", apiResponse.Code, currentResult.TotalCount)
	}

	site := dashboardTableBody(t, renderDashboard(t, r, "/?view=site"))
	if !strings.Contains(site, "West coast edge traffic evacuation") {
		t.Error("Site-wide table does not contain the site incident")
	}
	for _, excluded := range []string{"Ledger reconciliation backfill", "Orders archive compaction", "Ingress controller fleet replacement"} {
		if strings.Contains(site, excluded) {
			t.Errorf("Site-wide table unexpectedly contains %q", excluded)
		}
	}

	history := dashboardTableBody(t, renderDashboard(t, r, "/"))
	for _, want := range []string{
		"Checkout risk model advanced to cohort 40%",
		"2 links",
		"Checkout API 2026.08.24.3 rollout",
		"Checkout API 2026.08.24.3 rollout completed",
		"Legacy event without a logical operation ID",
	} {
		if !strings.Contains(history, want) {
			t.Errorf("History table does not contain %q", want)
		}
	}
	if strings.Contains(history, "Ledger reconciliation backfill") {
		t.Error("default 24-hour History contains the two-week-old operation")
	}

	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?external_id=demo-history-release", nil)
	listResponse := httptest.NewRecorder()
	r.ServeHTTP(listResponse, listRequest)
	var historyResult model.ListResult
	if err := json.NewDecoder(listResponse.Body).Decode(&historyResult); err != nil {
		t.Fatalf("decode event list for detail link check: %v", err)
	}
	var linkedEventID string
	for _, event := range historyResult.Events {
		if event.ExternalID == "demo-history-release" {
			linkedEventID = event.ID
			break
		}
	}
	if linkedEventID == "" {
		t.Fatal("fixture event with links not found")
	}
	detail := renderDashboard(t, r, "/events/"+linkedEventID)
	for _, want := range []string{
		`href="https://github.com/example/checkout/pull/482"`,
		`>Rollout PR</a>`,
		`href="https://example.pagerduty.com/incidents/PDEMO"`,
		`>PagerDuty incident</a>`,
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("Detail response does not contain link markup %q", want)
		}
	}

	alerts := dashboardTableBody(t, renderDashboard(t, r, "/?view=alerts"))
	if !strings.Contains(alerts, "Checkout risk model advanced to cohort 40%") {
		t.Error("Alerts table does not contain the actively alerted history event")
	}

	var activeEventID string
	for _, event := range currentResult.Events {
		if event.ExternalID == "demo-payments-sev1-start" {
			activeEventID = event.ID
			break
		}
	}
	if activeEventID == "" {
		t.Fatal("active fixture event for action workflow not found")
	}
	openDetail := renderDashboard(t, r, "/events/"+activeEventID)
	if !strings.Contains(openDetail, `action="/events/`+activeEventID+`/close"`) || !strings.Contains(openDetail, ">open<") {
		t.Error("active event detail does not expose its close action and open state")
	}
	for _, want := range []string{"Key rotation runbook", "Change plan PR", "added external links"} {
		if !strings.Contains(openDetail, want) {
			t.Errorf("seeded active event detail does not contain %q", want)
		}
	}

	linksResponse := performAPIRequest(t, r, http.MethodPost, "/api/v1/events/"+activeEventID+"/links", `{
		"user_name":"on-call",
		"links":[
			{"label":"Rotation plan","url":"https://notion.so/example/rotation-plan"},
			{"label":"Implementation PR","url":"https://github.com/example/card-vault/pull/77"},
			{"label":"<img src=x onerror=alert(1)>","url":"https://example.com/safe-target"}
		]
	}`)
	if linksResponse.Code != http.StatusCreated {
		t.Fatalf("append links status = %d, body = %s", linksResponse.Code, linksResponse.Body.String())
	}
	alertResponse := performAPIRequest(t, r, http.MethodPost, "/api/v1/events/"+activeEventID+"/alert", "")
	if alertResponse.Code != http.StatusCreated {
		t.Fatalf("toggle alert status = %d, body = %s", alertResponse.Code, alertResponse.Body.String())
	}
	actionDetail := renderDashboard(t, r, "/events/"+activeEventID)
	for _, want := range []string{"Rotation plan", "Implementation PR", "Activity", "Clear alert"} {
		if !strings.Contains(actionDetail, want) {
			t.Errorf("action detail does not contain %q", want)
		}
	}
	if strings.Contains(actionDetail, `<img src=x onerror=alert(1)>`) || !strings.Contains(actionDetail, `&lt;img src=x onerror=alert(1)&gt;`) {
		t.Error("link label markup was not safely escaped")
	}

	closeResponse := performAPIRequest(t, r, http.MethodPost, "/api/v1/events/"+activeEventID+"/close", `{
		"user_name":"release-manager",
		"description":"Key rotation completed and verified"
	}`)
	if closeResponse.Code != http.StatusCreated {
		t.Fatalf("close operation status = %d, body = %s", closeResponse.Code, closeResponse.Body.String())
	}
	closedDetail := renderDashboard(t, r, "/events/"+activeEventID)
	if !strings.Contains(closedDetail, ">closed<") || !strings.Contains(closedDetail, "Key rotation completed and verified") || strings.Contains(closedDetail, `action="/events/`+activeEventID+`/close"`) {
		t.Error("closed event detail has the wrong lifecycle state or still offers close")
	}
	currentAfterClose := performAPIRequest(t, r, http.MethodGet, "/api/v1/current?for_team=payments", "")
	var reducedAfterClose model.ListResult
	if err := json.NewDecoder(currentAfterClose.Body).Decode(&reducedAfterClose); err != nil {
		t.Fatalf("decode current after close: %v", err)
	}
	if currentAfterClose.Code != http.StatusOK || reducedAfterClose.TotalCount != currentResult.TotalCount-1 {
		t.Errorf("current after close = status %d total %d, want 200 and %d", currentAfterClose.Code, reducedAfterClose.TotalCount, currentResult.TotalCount-1)
	}

	fontRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/fonts/Orbitron-Variable.ttf", nil)
	fontResponse := httptest.NewRecorder()
	r.ServeHTTP(fontResponse, fontRequest)
	if fontResponse.Code != http.StatusOK || fontResponse.Body.Len() < 1000 {
		t.Errorf("vendored font response = status %d, bytes %d", fontResponse.Code, fontResponse.Body.Len())
	}

	cssRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/style.css", nil)
	cssResponse := httptest.NewRecorder()
	r.ServeHTTP(cssResponse, cssRequest)
	if cssResponse.Code != http.StatusOK || !strings.Contains(cssResponse.Body.String(), "/static/fonts/ChakraPetch-Regular.ttf") {
		t.Errorf("stylesheet does not reference the local body font; status = %d", cssResponse.Code)
	}

	jsRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/static/form-validation.js", nil)
	jsResponse := httptest.NewRecorder()
	r.ServeHTTP(jsResponse, jsRequest)
	if jsResponse.Code != http.StatusOK || !strings.Contains(jsResponse.Body.String(), "function validateLinks") ||
		!strings.Contains(jsResponse.Body.String(), "function eventTagsError") {
		t.Errorf("form validation script response = status %d; expected embedded validator", jsResponse.Code)
	}
}

func TestSeededDashboardTeamTagFilters(t *testing.T) {
	t.Parallel()

	dashboard := seededDashboardRouter(t)
	tests := []struct {
		team     string
		included []string
		excluded []string
	}{
		{
			team: "payments",
			included: []string{
				"Checkout risk model advanced to cohort 40%",
				"Primary card vault key rotation",
			},
			excluded: []string{
				"West coast edge traffic evacuation",
				"Ingress controller fleet replacement",
			},
		},
		{
			team: "platform",
			included: []string{
				"West coast edge traffic evacuation",
				"Ingress controller fleet replacement",
			},
			excluded: []string{
				"Checkout risk model advanced to cohort 40%",
				"Primary card vault key rotation",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.team, func(t *testing.T) {
			t.Parallel()

			page := renderDashboard(t, dashboard, "/?tag="+url.QueryEscape("team:"+tc.team))
			if want := "<span>Team</span><strong>" + tc.team + "</strong>"; !strings.Contains(page, want) {
				t.Errorf("team summary does not contain %q", want)
			}
			if want := "Filtering by tags:</span>\n    <span class=\"badge badge-active\">team:" + tc.team; !strings.Contains(page, want) {
				t.Errorf("active-filter display does not contain %q", want)
			}

			table := dashboardTableBody(t, page)
			for _, want := range tc.included {
				if !strings.Contains(table, want) {
					t.Errorf("%s team table does not contain %q", tc.team, want)
				}
			}
			for _, unwanted := range tc.excluded {
				if strings.Contains(table, unwanted) {
					t.Errorf("%s team table unexpectedly contains %q", tc.team, unwanted)
				}
			}
		})
	}
}

func TestRecordChangeFormTreatsSQLAsData(t *testing.T) {
	t.Parallel()

	dashboard := seededDashboardRouter(t)
	payload := `value'); DROP TABLE change_events; --`
	response := performDashboardForm(t, dashboard, "/events", url.Values{
		"user_name":        {payload},
		"event_type":       {payload},
		"description":      {payload},
		"long_description": {payload},
		"external_id":      {payload},
		"tags":             {payload + "=" + payload},
		"link_label":       {payload},
		"link_url":         {"https://example.com/?q=%27%3Bdrop"},
	})
	if response.Code != http.StatusSeeOther {
		t.Fatalf("record SQL-looking change status = %d, want %d; body = %s", response.Code, http.StatusSeeOther, response.Body.String())
	}
	location := response.Header().Get("Location")
	eventID, ok := strings.CutPrefix(location, "/events/")
	if !ok || eventID == "" {
		t.Fatalf("record SQL-looking change Location = %q, want event detail path", location)
	}

	readback := performAPIRequest(t, dashboard, http.MethodGet, "/api/v1/events/"+eventID, "")
	if readback.Code != http.StatusOK {
		t.Fatalf("read SQL-looking change status = %d, want %d; body = %s", readback.Code, http.StatusOK, readback.Body.String())
	}
	var event model.ChangeEvent
	if err := json.NewDecoder(readback.Body).Decode(&event); err != nil {
		t.Fatalf("decode SQL-looking change: %v", err)
	}
	if event.EventType != payload || event.Description != payload || event.LongDescription != payload || event.ExternalID != payload {
		t.Errorf("SQL-looking event fields = %#v, want payload preserved", event)
	}
	if event.UserName != "alice" {
		t.Errorf("SQL-looking event UserName = %q, want authenticated user alice", event.UserName)
	}
	if event.Tags[payload] != payload {
		t.Errorf("SQL-looking event Tags[%q] = %q, want payload", payload, event.Tags[payload])
	}
	if len(event.Links) != 1 || event.Links[0].Label != payload {
		t.Errorf("SQL-looking event Links = %#v, want payload label", event.Links)
	}

	probe := performDashboardForm(t, dashboard, "/events", url.Values{
		"event_type":  {"deployment"},
		"description": {"Database remains operational"},
	})
	if probe.Code != http.StatusSeeOther {
		t.Fatalf("record probe change status = %d, want %d; body = %s", probe.Code, http.StatusSeeOther, probe.Body.String())
	}
}

type seededDashboard struct {
	handler       http.Handler
	cookie        *http.Cookie
	sessionSecret []byte
}

func (d *seededDashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.AddCookie(d.cookie)
	d.handler.ServeHTTP(w, r)
}

func seededDashboardRouter(t *testing.T) *seededDashboard {
	t.Helper()

	databaseURL := functionalDatabaseURL(t)
	if err := postgresdb.Migrate(databaseURL, time.Second); err != nil {
		t.Fatalf("migrate functional test database: %v", err)
	}
	pool, err := postgresdb.Open(t.Context(), databaseURL, postgresdb.PoolOptions{
		MaxConnections: 5,
		ConnectTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open functional test database: %v", err)
	}
	t.Cleanup(pool.Close)

	store := postgresstore.New(pool, time.Second)
	svc := service.NewChangeService(store)
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve functional test path")
	}
	fixturePath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "functional", "phosphor-demo.json")
	file, err := os.Open(fixturePath) //nolint:gosec // G304: path is rooted at this test source file
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	events, err := fixture.Load(file)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if _, err := fixture.Apply(t.Context(), svc, events); err != nil {
		t.Fatalf("apply fixture: %v", err)
	}

	sessionSecret := []byte("functional-test-session-secret-32b")
	apiHandler := handler.NewAPIHandler(svc, pool)
	dashboardHandler := handler.NewDashboardHandler(svc, 0, sessionSecret)
	authenticator := humanauth.NewGitHub(humanauth.ProviderOptions{ClientID: "test", AllowAny: true})
	humanAuthHandler := handler.NewHumanAuthHandler(authenticator, handler.HumanAuthOptions{
		SessionSecret: sessionSecret, SessionDuration: time.Hour,
	})
	mux := router.New(apiHandler, dashboardHandler, humanAuthHandler, &config.Config{
		APITokens:         []string{"demo-token"},
		RequireAuthReads:  false,
		SessionSecret:     sessionSecret,
		HumanAuthProvider: "github",
	})
	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: sessionSecret, Duration: time.Hour,
	}, humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"}); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := cookieRecorder.Result().Cookies()[0]
	return &seededDashboard{handler: mux, cookie: cookie, sessionSecret: sessionSecret}
}

func functionalDatabaseURL(t *testing.T) string {
	t.Helper()
	databaseURL := os.Getenv("PCR_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("PCR_TEST_POSTGRES_URL is not set")
	}

	admin, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL test admin pool: %v", err)
	}
	t.Cleanup(admin.Close)

	schema := "pcr_dashboard_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create dashboard test schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop dashboard test schema %q: %v", schema, err)
		}
	})

	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse PCR_TEST_POSTGRES_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func renderDashboard(t *testing.T, handler http.Handler, path string) string {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d; body = %s", path, response.Code, response.Body.String())
	}
	return response.Body.String()
}

func performAPIRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer demo-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performDashboardForm(t *testing.T, dashboard *seededDashboard, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()

	sessionRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	sessionRequest.AddCookie(dashboard.cookie)
	session, err := middleware.ReadHumanSession(sessionRequest, dashboard.sessionSecret, "github")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	values.Set("csrf_token", middleware.GenerateCSRFToken(dashboard.sessionSecret, session.Nonce))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	dashboard.ServeHTTP(response, request)
	return response
}

func dashboardTableBody(t *testing.T, page string) string {
	t.Helper()
	_, after, ok := strings.Cut(page, "<tbody>")
	if !ok {
		t.Fatal("dashboard response has no table body")
	}
	body, _, ok := strings.Cut(after, "</tbody>")
	if !ok {
		t.Fatal("dashboard response has no closing table body")
	}
	return body
}

func dashboardBannerBody(t *testing.T, page string) string {
	t.Helper()

	_, after, ok := strings.Cut(page, `<section class="severity-banner`)
	if !ok {
		t.Fatal("dashboard response has no high-visibility banner")
	}
	body, _, ok := strings.Cut(after, "</section>")
	if !ok {
		t.Fatal("dashboard response has no closing high-visibility banner section")
	}
	return body
}
