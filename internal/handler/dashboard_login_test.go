package handler_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// loginStack holds the components for LoginHandler tests.
type loginStack struct {
	handler *handler.LoginHandler
	router  chi.Router
}

func newLoginTestStack(tokens []string, sessionOpts middleware.SessionOptions) *loginStack {
	h := handler.NewLoginHandler(tokens, sessionOpts)
	r := chi.NewRouter()
	r.Get("/login", h.ShowLoginForm)
	r.Post("/login", h.Login)
	return &loginStack{handler: h, router: r}
}

var dashboardSessionSecret = []byte("test-session-secret")

// dashboardStack holds the components for DashboardHandler tests.
type dashboardStack struct {
	store   *mockStore
	service *service.ChangeService
	handler *handler.DashboardHandler
	router  chi.Router
}

func newDashboardTestStack() *dashboardStack {
	ms := &mockStore{
		listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{Events: []model.ChangeEvent{}, Limit: params.EffectiveLimit()}, nil
		},
		listCurrentFn: func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.EffectiveLimit(),
				Offset:     params.Offset,
			}, nil
		},
	}
	svc := service.NewChangeService(ms)
	h := handler.NewDashboardHandler(svc, 60, dashboardSessionSecret)

	r := chi.NewRouter()
	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: dashboardSessionSecret, Duration: time.Hour,
	}, humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"}); err != nil {
		panic(err)
	}
	testCookie := cookieRecorder.Result().Cookies()[0]
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie(middleware.SessionCookieName); err != nil {
				r.AddCookie(testCookie)
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Use(middleware.RequireHumanAuth(dashboardSessionSecret, "github"))
	r.Get("/", h.Dashboard)
	r.Get("/events/new", h.ShowCreateEvent)
	r.Post("/events", h.CreateEvent)
	r.Get("/events/{id}", h.Detail)
	r.Post("/events/{id}/star", h.ToggleStar)
	r.Post("/events/{id}/alert", h.ToggleAlert)
	r.Post("/events/{id}/links", h.AddLinks)
	r.Post("/events/{id}/close", h.CloseOperation)

	return &dashboardStack{
		store:   ms,
		service: svc,
		handler: h,
		router:  r,
	}
}

// addCSRFToRequest creates a valid session cookie and CSRF form body for POST tests.
// The CSRF token is written into the request body as application/x-www-form-urlencoded
// so it survives ParseForm -- mirroring how real browsers submit dashboard forms.
func addCSRFToRequest(t *testing.T, req *http.Request) {
	addCSRFFormToRequest(t, req, url.Values{})
}

func addCSRFFormToRequest(t *testing.T, req *http.Request, values url.Values) {
	t.Helper()
	rec := httptest.NewRecorder()
	err := middleware.SetHumanSessionCookie(rec, middleware.HumanSessionOptions{
		Secret: dashboardSessionSecret, Duration: time.Hour,
	}, humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"})
	if err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	session, err := middleware.ReadHumanSession(req, dashboardSessionSecret, "github")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}
	csrfToken := middleware.GenerateCSRFToken(dashboardSessionSecret, session.Nonce)

	values.Set("csrf_token", csrfToken)
	body := values.Encode()
	req.Body = io.NopCloser(strings.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
}

// ---------- LoginHandler ----------

func TestLogin(t *testing.T) {
	t.Parallel()

	loginOpts := middleware.SessionOptions{Secret: []byte("test-secret")}

	t.Run("GET shows login form", func(t *testing.T) {
		t.Parallel()

		ls := newLoginTestStack([]string{"valid-token-1"}, loginOpts)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		ls.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `name="token"`) {
			t.Error("expected login form with token input field")
		}
	})

	t.Run("valid POST sets session cookie and redirects", func(t *testing.T) {
		t.Parallel()

		ls := newLoginTestStack([]string{"valid-token-1"}, loginOpts)
		body := strings.NewReader("token=valid-token-1")
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		ls.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("expected Location /, got %q", loc)
		}

		var foundCookie bool
		for _, c := range rec.Result().Cookies() {
			if c.Name == middleware.SessionCookieName {
				foundCookie = true
				if c.Value == "" {
					t.Error("expected non-empty cookie value")
				}
				if !c.HttpOnly {
					t.Error("expected HttpOnly to be true")
				}
				if c.Path != "/" {
					t.Errorf("expected Path /, got %q", c.Path)
				}
				break
			}
		}
		if !foundCookie {
			t.Fatal("expected pcr_session cookie to be set")
		}
	})

	t.Run("second token in multi-token list works", func(t *testing.T) {
		t.Parallel()

		ls := newLoginTestStack([]string{"first-token", "second-token"}, loginOpts)
		body := strings.NewReader("token=second-token")
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		ls.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
	})

	unauthorizedCases := []struct {
		name string
		body string
	}{
		{"missing token", ""},
		{"invalid token", "token=wrong-token"},
		{"empty token", "token="},
	}
	for _, tc := range unauthorizedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ls := newLoginTestStack([]string{"valid-token-1"}, loginOpts)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			ls.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d; body: %s", rec.Code, rec.Body.String())
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == middleware.SessionCookieName {
					t.Fatal("expected no session cookie, but found one")
				}
			}
		})
	}

	t.Run("oversized form body rejected with 413", func(t *testing.T) {
		t.Parallel()

		ls := newLoginTestStack([]string{"valid-token-1"}, loginOpts)
		// Body well above the 8 KiB cap; the auth check must not run.
		oversized := "token=" + strings.Repeat("a", 16<<10)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/login", strings.NewReader(oversized))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		ls.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d; body: %s", rec.Code, rec.Body.String())
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == middleware.SessionCookieName {
				t.Fatal("expected no session cookie when body is rejected")
			}
		}
	})
}

// ---------- DashboardHandler.Dashboard ----------

func TestDashboard(t *testing.T) {
	t.Parallel()

	// emptyListFn returns an empty result and is reused across subtests.
	emptyListFn := func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
		return &model.ListResult{
			Events:     []model.ChangeEvent{},
			TotalCount: 0,
			Limit:      params.Limit,
			Offset:     params.Offset,
		}, nil
	}
	emptyAnnotationsBatchFn := func(_ context.Context, _ []string) (map[string]*model.EventAnnotations, error) {
		return map[string]*model.EventAnnotations{}, nil
	}

	t.Run("empty event list returns 200 with HTML content type", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.listFn = emptyListFn
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Errorf("expected Content-Type text/html, got %q", ct)
		}
		if !strings.Contains(rec.Body.String(), "No events found.") {
			t.Error("expected body to contain 'No events found.'")
		}
	})

	t.Run("renders event data in response body", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		now := time.Now().UTC()
		ds.store.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{
				Events: []model.ChangeEvent{{
					ID:          "evt-dash-001",
					UserName:    "alice",
					EventType:   "deployment",
					Description: "deploy widget-service v3.7",
					Timestamp:   now,
					CreatedAt:   now,
				}},
				TotalCount: 1,
				Limit:      40,
				Offset:     0,
			}, nil
		}
		ds.store.getAnnotationsBatchFn = func(_ context.Context, _ []string) (map[string]*model.EventAnnotations, error) {
			return map[string]*model.EventAnnotations{
				"evt-dash-001": {Starred: false, Alerted: false},
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"deploy widget-service v3.7",
			"alice",
			"deployment",
			"evt-dash-001",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("expected body to contain %q", want)
			}
		}
	})

	t.Run("passes filter params to service", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var captured model.ListParams
		ds.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{Events: []model.ChangeEvent{}, TotalCount: 0, Limit: params.Limit, Offset: params.Offset}, nil
		}
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?type=deployment&user=alice&range=24h", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if captured.EventType != "deployment" {
			t.Errorf("EventType = %q, want %q", captured.EventType, "deployment")
		}
		if captured.UserName != "alice" {
			t.Errorf("UserName = %q, want %q", captured.UserName, "alice")
		}
		if !captured.TopLevel {
			t.Error("expected TopLevel to be true")
		}
	})

	t.Run("default time range is 24h", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var captured model.ListParams
		ds.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{Events: []model.ChangeEvent{}, TotalCount: 0, Limit: params.Limit, Offset: params.Offset}, nil
		}
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if captured.StartAfter == nil {
			t.Fatal("expected StartAfter to be set")
		}
		expected := time.Now().UTC().Add(-24 * time.Hour)
		diff := captured.StartAfter.Sub(expected)
		if diff < -2*time.Second || diff > 2*time.Second {
			t.Fatalf("expected StartAfter ~%v, got %v (diff %v)", expected, *captured.StartAfter, diff)
		}
		if captured.StartBefore != nil {
			t.Error("expected StartBefore to be nil for default range")
		}
	})

	t.Run("custom time range", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var captured model.ListParams
		ds.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{Events: []model.ChangeEvent{}, TotalCount: 0, Limit: params.Limit, Offset: params.Offset}, nil
		}
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?range=custom&start_after=2026-01-01T00:00&start_before=2026-01-02T00:00", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if captured.StartAfter == nil {
			t.Fatal("expected StartAfter to be set")
		}
		wantAfter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if !captured.StartAfter.Equal(wantAfter) {
			t.Errorf("StartAfter = %v, want %v", *captured.StartAfter, wantAfter)
		}
		if captured.StartBefore == nil {
			t.Fatal("expected StartBefore to be set")
		}
		wantBefore := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		if !captured.StartBefore.Equal(wantBefore) {
			t.Errorf("StartBefore = %v, want %v", *captured.StartBefore, wantBefore)
		}
	})

	t.Run("pagination parameters and links", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var captured model.ListParams
		ds.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			events := make([]model.ChangeEvent, 20)
			for i := range events {
				events[i] = model.ChangeEvent{
					ID:        fmt.Sprintf("evt-page-%03d", i),
					EventType: "deployment",
					Timestamp: time.Now().UTC(),
					CreatedAt: time.Now().UTC(),
				}
			}
			return &model.ListResult{
				Events:     events,
				TotalCount: 100,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}
		ds.store.getAnnotationsBatchFn = func(_ context.Context, _ []string) (map[string]*model.EventAnnotations, error) {
			return map[string]*model.EventAnnotations{}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?offset=40&limit=20", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if captured.Offset != 40 {
			t.Errorf("Offset = %d, want 40", captured.Offset)
		}
		if captured.Limit != 20 {
			t.Errorf("Limit = %d, want 20", captured.Limit)
		}

		body := rec.Body.String()
		if !strings.Contains(body, "Previous") {
			t.Error("expected body to contain 'Previous' pagination link")
		}
		if !strings.Contains(body, "Next") {
			t.Error("expected body to contain 'Next' pagination link")
		}
		if !strings.Contains(body, "Showing 41") {
			t.Error("expected body to contain 'Showing 41'")
		}
	})

	t.Run("service error returns 500 without leaking internals", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			return nil, errors.New("database connection lost")
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "database connection lost") {
			t.Error("internal error message leaked to response body")
		}
	})

	t.Run("current view uses current query and renders independent banner", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		historyCalled := false
		ds.store.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			historyCalled = true
			return &model.ListResult{}, nil
		}
		var tableParams, bannerParams model.CurrentParams
		ds.store.listCurrentFn = func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			if params.Offset == 0 {
				bannerParams = params
				return &model.ListResult{
					Events: []model.ChangeEvent{{
						ID:          "site-incident",
						EventType:   "deployment",
						Description: "database failover in progress",
						Timestamp:   time.Now().UTC().Add(-time.Hour),
						Tags:        map[string]string{"team": "payments", "scope": "site", "severity": "sev2"},
					}},
					TotalCount: 2,
					Limit:      params.Limit,
				}, nil
			}

			tableParams = params
			return &model.ListResult{
				Events: []model.ChangeEvent{{
					ID:          "payments-change",
					EventType:   "deployment",
					Description: "payments rollout",
					Timestamp:   time.Now().UTC().Add(-30 * time.Minute),
					Tags:        map[string]string{"team": "payments", "scope": "service", "severity": "sev2"},
				}},
				TotalCount: 3,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/?view=current&team=payments&type=deployment&scope=site&severity=sev2&limit=1&offset=1",
			nil,
		)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if historyCalled {
			t.Error("current view called historical List")
		}
		if tableParams.ForTeam != "payments" || tableParams.EventType != "deployment" || tableParams.Limit != 1 || tableParams.Offset != 1 {
			t.Errorf("table params = %+v", tableParams)
		}
		if len(tableParams.Severities) != 1 || tableParams.Severities[0] != "sev2" {
			t.Errorf("table severities = %v, want [sev2]", tableParams.Severities)
		}
		if len(tableParams.Scopes) != 1 || tableParams.Scopes[0] != "site" {
			t.Errorf("table scopes = %v, want [site]", tableParams.Scopes)
		}
		if bannerParams.ForTeam != "payments" || bannerParams.EventType != "deployment" || bannerParams.Limit <= tableParams.Limit {
			t.Errorf("banner params = %+v, want selected team/type and independent bounded query", bannerParams)
		}
		if len(bannerParams.Severities) != 1 || bannerParams.Severities[0] != "sev2" {
			t.Errorf("banner severities = %v, want [sev2]", bannerParams.Severities)
		}
		if len(bannerParams.Scopes) != 1 || bannerParams.Scopes[0] != "site" {
			t.Errorf("banner scopes = %v, want [site]", bannerParams.Scopes)
		}

		body := rec.Body.String()
		for _, want := range []string{
			"Active high-visibility changes",
			"database failover in progress",
			"Site-wide",
			"payments rollout",
			"Maintenance windows",
			`list="event-types"`,
			`name="scope" value="site" checked`,
			`name="severity" value="sev2" checked`,
			"Current",
			"Next",
			`href="/?scope=site&amp;severity=sev2&amp;team=payments&amp;type=deployment&amp;view=current" class="banner-more"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("body does not contain %q", want)
			}
		}
	})

	t.Run("site view forces site scope", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var siteParams, bannerParams model.CurrentParams
		ds.store.listCurrentFn = func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			if len(params.Severities) == 2 {
				bannerParams = params
			} else {
				siteParams = params
			}
			return &model.ListResult{Events: []model.ChangeEvent{}, Limit: params.Limit}, nil
		}
		ds.store.getAnnotationsBatchFn = emptyAnnotationsBatchFn

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?view=site&team=payments", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if len(siteParams.Scopes) != 1 || siteParams.Scopes[0] != "site" {
			t.Errorf("site scopes = %v, want [site]", siteParams.Scopes)
		}
		if siteParams.ForTeam != "" {
			t.Errorf("site ForTeam = %q, want empty", siteParams.ForTeam)
		}
		if len(bannerParams.Scopes) != 1 || bannerParams.Scopes[0] != "site" {
			t.Errorf("banner scopes = %v, want [site]", bannerParams.Scopes)
		}
		if len(bannerParams.Severities) != 2 || bannerParams.Severities[0] != "sev0" || bannerParams.Severities[1] != "sev1" {
			t.Errorf("banner severities = %v, want [sev0 sev1]", bannerParams.Severities)
		}
	})

	t.Run("banner error fails the page", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.listFn = emptyListFn
		ds.store.listCurrentFn = func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
			return nil, errors.New("banner query failed")
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "banner query failed") {
			t.Error("internal banner error leaked to response")
		}
	})
}

// ---------- DashboardHandler.Detail ----------

func TestDetail(t *testing.T) {
	t.Parallel()

	t.Run("existing event returns 200 with event details", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		now := time.Now().UTC()
		ds.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-detail-001" {
				return &model.ChangeEvent{
					ID:          "evt-detail-001",
					UserName:    "bob",
					EventType:   "feature-flag",
					Description: "enabled dark-mode flag",
					Links:       []model.EventLink{{Label: "Original plan", URL: "https://notion.so/example/original"}},
					Timestamp:   now,
					CreatedAt:   now,
				}, nil
			}
			return nil, nil
		}
		ds.store.getAnnotationsFn = func(_ context.Context, _ string) (*model.EventAnnotations, error) {
			return &model.EventAnnotations{Starred: true, Alerted: false}, nil
		}
		ds.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{Events: []model.ChangeEvent{{
				ID:          "link-note-1",
				ParentID:    params.ParentID,
				UserName:    "alice",
				EventType:   model.EventTypeLink,
				Description: "added external links",
				Links:       []model.EventLink{{Label: "Incident", URL: "https://example.pagerduty.com/incidents/P1"}},
				Timestamp:   now.Add(time.Minute),
			}}, TotalCount: 1}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/evt-detail-001", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Errorf("expected Content-Type text/html, got %q", ct)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"enabled dark-mode flag",
			"bob",
			"feature-flag",
			"Starred",
			"Original plan",
			"Incident",
			"Activity",
			`action="/events/evt-detail-001/links"`,
			`action="/events/evt-detail-001/alert"`,
			`data-add-links-form`,
			`src="/static/form-validation.js"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("expected body to contain %q", want)
			}
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/nonexistent", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("service error returns 500 without leaking internals", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, errors.New("disk I/O error")
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/evt-err", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "disk I/O error") {
			t.Error("internal error message leaked to response body")
		}
	})
}

func TestDashboardEventActions(t *testing.T) {
	t.Parallel()

	t.Run("adds multiple links", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "event-1"}, nil
		}
		var annotation *model.ChangeEvent
		ds.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			annotation = event
			return event, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/event-1/links", nil)
		req.Header.Set("Referer", "/events/event-1")
		addCSRFFormToRequest(t, req, url.Values{
			"link_label": {"Plan", "PR"},
			"link_url":   {"https://notion.so/example/plan", "https://github.com/example/repo/pull/1"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if annotation == nil || len(annotation.Links) != 2 || annotation.Links[1].Label != "PR" {
			t.Errorf("annotation = %+v", annotation)
		}
		if annotation.UserName != "alice" || annotation.UserProvider != "github" || annotation.UserSubject != "12345" {
			t.Errorf("annotation identity = %q/%q/%q, want alice/github/12345", annotation.UserName, annotation.UserProvider, annotation.UserSubject)
		}
	})

	t.Run("rejects unsafe link", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "event-1", EventType: "deployment", Description: "Deploy service"}, nil
		}
		ds.store.getAnnotationsFn = func(_ context.Context, _ string) (*model.EventAnnotations, error) {
			return &model.EventAnnotations{}, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/event-1/links", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"link_label": {"Release plan", "deceptive <link>"},
			"link_url":   {"https://example.com/release-plan", "https://trusted.example@evil.example/path"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", contentType)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"Link must be an absolute HTTP or HTTPS URL without credentials",
			`value="Release plan"`,
			`value="https://example.com/release-plan"`,
			`value="deceptive &lt;link&gt;"`,
			`value="https://trusted.example@evil.example/path"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("validation response does not contain %q", want)
			}
		}
		if strings.Contains(body, "deceptive <link>") {
			t.Error("validation response did not escape the submitted label")
		}
	})

	t.Run("toggles alert", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.toggleAlertFn = func(_ context.Context, eventID, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ParentID: eventID, EventType: model.EventTypeAlert}, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/event-1/alert", nil)
		addCSRFToRequest(t, req)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("closes operation", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		start := &model.ChangeEvent{ID: "start-1", EventType: "deployment", Tags: map[string]string{"phase": "start", "change_id": "change-1"}}
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) { return start, nil }
		ds.store.listCurrentFn = func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
			return &model.ListResult{TotalCount: 1}, nil
		}
		ds.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) { return event, nil }
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/start-1/close", nil)
		addCSRFFormToRequest(t, req, url.Values{"description": {"completed after checks"}})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
}

func TestDashboardRecordChange(t *testing.T) {
	t.Parallel()

	t.Run("GET renders the record change form", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/events/new", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("GET /events/new status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			`data-interface="phosphor-deck"`,
			`href="/events/new"`,
			`action="/events"`,
			`name="csrf_token"`,
			`name="event_type"`,
			`name="description"`,
			`name="long_description"`,
			`name="external_id"`,
			`name="tags"`,
			`name="link_label"`,
			`name="link_url"`,
			`data-record-form`,
			`data-link-row`,
			`data-maintenance-guidance`,
			`src="/static/form-validation.js"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("GET /events/new body does not contain %q", want)
			}
		}
		if strings.Contains(body, `name="timestamp"`) {
			t.Error("GET /events/new exposes a caller-controlled timestamp")
		}
	})

	t.Run("invalid initial link re-renders entered values", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"event_type":  {"deployment"},
			"description": {"Deploy checkout"},
			"link_label":  {"Runbook <draft>"},
			"link_url":    {"javascript:alert(1)"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /events invalid link status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range []string{
			"Link must be an absolute HTTP or HTTPS URL without credentials",
			`value="Runbook &lt;draft&gt;"`,
			`value="javascript:alert(1)"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("validation response does not contain %q", want)
			}
		}
	})

	t.Run("duplicate external ID redirects to the existing event", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		existing := &model.ChangeEvent{ID: "existing-event"}
		ds.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return existing, store.ErrDuplicate
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"event_type":  {"deployment"},
			"description": {"Duplicate delivery"},
			"external_id": {"deploy-123"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /events duplicate status = %d, want %d; body = %s", rec.Code, http.StatusSeeOther, rec.Body.String())
		}
		if location := rec.Header().Get("Location"); location != "/events/existing-event" {
			t.Errorf("POST /events duplicate Location = %q, want %q", location, "/events/existing-event")
		}
	})

	t.Run("POST creates an attributed event and redirects to detail", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var created *model.ChangeEvent
		ds.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			created = event
			return event, nil
		}
		before := time.Now().UTC()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"user_name":        {"mallory"},
			"event_type":       {" deployment "},
			"description":      {" Deploy payments v2.5.0 "},
			"long_description": {"Rolling update across three regions."},
			"external_id":      {" github-actions-2468 "},
			"timestamp":        {"2001-01-01T00:00"},
			"tags":             {"team=payments\nscope=service\nseverity=sev2"},
			"link_label":       {"Release PR"},
			"link_url":         {"https://github.com/example/payments/pull/2468"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)
		after := time.Now().UTC()

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("POST /events status = %d, want %d; body = %s", rec.Code, http.StatusSeeOther, rec.Body.String())
		}
		if created == nil {
			t.Fatal("POST /events did not create an event")
		}
		if location := rec.Header().Get("Location"); location != "/events/"+created.ID {
			t.Errorf("POST /events Location = %q, want %q", location, "/events/"+created.ID)
		}
		if created.UserName != "alice" || created.UserProvider != "github" || created.UserSubject != "12345" {
			t.Errorf("created identity = %q/%q/%q, want alice/github/12345", created.UserName, created.UserProvider, created.UserSubject)
		}
		if created.EventType != "deployment" || created.Description != "Deploy payments v2.5.0" || created.ExternalID != "github-actions-2468" {
			t.Errorf("created core fields = type %q, description %q, external ID %q", created.EventType, created.Description, created.ExternalID)
		}
		if created.Timestamp.Before(before) || created.Timestamp.After(after) {
			t.Errorf("created timestamp = %s, want server time between %s and %s", created.Timestamp, before, after)
		}
		wantTags := map[string]string{"team": "payments", "scope": "service", "severity": "sev2"}
		if !maps.Equal(created.Tags, wantTags) {
			t.Errorf("created tags = %v, want %v", created.Tags, wantTags)
		}
		if len(created.Links) != 1 || created.Links[0].Label != "Release PR" || created.Links[0].URL != "https://github.com/example/payments/pull/2468" {
			t.Errorf("created links = %+v", created.Links)
		}
	})

	t.Run("maintenance without lifecycle tags re-renders validation error", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"event_type":  {"maintenance"},
			"description": {"WAF POP2"},
			"tags":        {"team=platform"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "maintenance events require") {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid input re-renders entered values", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		var createCalled bool
		ds.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			createCalled = true
			return event, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", nil)
		addCSRFFormToRequest(t, req, url.Values{
			"event_type":  {"deployment"},
			"description": {"Deploy <payments>"},
			"tags":        {"team=payments\nmissing-separator"},
		})
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /events invalid status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if createCalled {
			t.Error("POST /events created an event with invalid tags")
		}
		body := rec.Body.String()
		if !strings.Contains(body, "Tags must use one key=value pair per line") {
			t.Errorf("invalid response does not explain tag format: %s", body)
		}
		if !strings.Contains(body, "Deploy &lt;payments&gt;") || strings.Contains(body, "Deploy <payments>") {
			t.Error("invalid response did not preserve and escape the submitted description")
		}
	})

	t.Run("missing CSRF token is forbidden", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events", strings.NewReader("event_type=deployment&description=deploy"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST /events without CSRF status = %d, want %d", rec.Code, http.StatusForbidden)
		}
	})
}

// ---------- DashboardHandler.ToggleStar ----------
// Named TestDashboardToggleStar to avoid collision with TestToggleStar in api_test.go.

func TestDashboardToggleStar(t *testing.T) {
	t.Parallel()

	// setupToggleStarMocks configures the store for a successful star toggle.
	setupToggleStarMocks := func(ds *dashboardStack) {
		now := time.Now().UTC()
		ds.store.toggleStarFn = func(_ context.Context, eventID, userName string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{
				ID:        "star-1",
				ParentID:  eventID,
				UserName:  userName,
				EventType: model.EventTypeStar,
				Timestamp: now,
				CreatedAt: now,
			}, nil
		}
	}

	t.Run("successful toggle redirects to referer path only", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/evt-star-001/star", nil)
		req.Header.Set("Referer", "/events/evt-star-001")
		addCSRFToRequest(t, req)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if loc := rec.Header().Get("Location"); loc != "/events/evt-star-001" {
			t.Fatalf("expected Location /events/evt-star-001, got %q", loc)
		}
	})

	t.Run("external referer does not cause open redirect", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/evt-star-001/star", nil)
		req.Header.Set("Referer", "https://evil.com/phish")
		addCSRFToRequest(t, req)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		loc := rec.Header().Get("Location")
		if strings.Contains(loc, "evil.com") {
			t.Fatalf("redirect to external host: %q", loc)
		}
	})

	t.Run("ambiguous local paths do not cause open redirect", func(t *testing.T) {
		t.Parallel()

		tests := map[string]string{
			"double slash":         "https://registry.example//evil.com/phish",
			"encoded double slash": "https://registry.example/%2f%2fevil.com/phish",
			"backslash":            "https://registry.example/\\evil.com/phish",
		}
		for name, referer := range tests {
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				ds := newDashboardTestStack()
				setupToggleStarMocks(ds)

				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/evt-star-001/star", nil)
				req.Header.Set("Referer", referer)
				addCSRFToRequest(t, req)
				rec := httptest.NewRecorder()
				ds.router.ServeHTTP(rec, req)

				if rec.Code != http.StatusSeeOther {
					t.Fatalf("expected 303, got %d", rec.Code)
				}
				if loc := rec.Header().Get("Location"); loc != "/" {
					t.Fatalf("expected safe root redirect, got %q", loc)
				}
			})
		}
	})

	t.Run("no referer redirects to root", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/evt-star-001/star", nil)
		addCSRFToRequest(t, req)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("expected Location /, got %q", loc)
		}
	})

	t.Run("missing CSRF token returns 403", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/evt-star-001/star", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.toggleStarFn = func(_ context.Context, _, _ string) (*model.ChangeEvent, error) {
			return nil, store.ErrNotFound
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/events/nonexistent/star", nil)
		addCSRFToRequest(t, req)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})
}
