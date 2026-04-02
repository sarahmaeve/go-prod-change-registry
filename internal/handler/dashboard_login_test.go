package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
)

// loginStack holds the components for LoginHandler tests.
type loginStack struct {
	handler *handler.LoginHandler
	router  chi.Router
}

func newLoginTestStack(tokens []string, sessionSecret []byte) *loginStack {
	h := handler.NewLoginHandler(tokens, sessionSecret)
	r := chi.NewRouter()
	r.Get("/login", h.Login)
	return &loginStack{handler: h, router: r}
}

// dashboardStack holds the components for DashboardHandler tests.
type dashboardStack struct {
	store   *mockStore
	service *service.ChangeService
	handler *handler.DashboardHandler
	router  chi.Router
}

func newDashboardTestStack() *dashboardStack {
	ms := &mockStore{}
	svc := service.NewChangeService(ms)
	h := handler.NewDashboardHandler(svc, 60)

	r := chi.NewRouter()
	r.Get("/", h.Dashboard)
	r.Get("/events/{id}", h.Detail)
	r.Post("/events/{id}/star", h.ToggleStar)

	return &dashboardStack{
		store:   ms,
		service: svc,
		handler: h,
		router:  r,
	}
}

// ---------- LoginHandler ----------

func TestLogin(t *testing.T) {
	t.Parallel()

	t.Run("valid token sets session cookie and redirects", func(t *testing.T) {
		t.Parallel()

		ls := newLoginTestStack([]string{"valid-token-1"}, []byte("test-secret"))
		req := httptest.NewRequest(http.MethodGet, "/login?token=valid-token-1", nil)
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

	unauthorizedCases := []struct {
		name  string
		query string
	}{
		{"missing token", "/login"},
		{"invalid token", "/login?token=wrong-token"},
		{"empty token", "/login?token="},
	}
	for _, tc := range unauthorizedCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ls := newLoginTestStack([]string{"valid-token-1"}, []byte("test-secret"))
			req := httptest.NewRequest(http.MethodGet, tc.query, nil)
			rec := httptest.NewRecorder()
			ls.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			for _, c := range rec.Result().Cookies() {
				if c.Name == middleware.SessionCookieName {
					t.Fatal("expected no session cookie, but found one")
				}
			}
		})
	}
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

		req := httptest.NewRequest(http.MethodGet, "/", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/?type=deployment&user=alice&range=24h", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/?range=custom&start_after=2026-01-01T00:00&start_before=2026-01-02T00:00", nil)
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
					ID:        "evt-page-" + strings.Repeat("0", i),
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

		req := httptest.NewRequest(http.MethodGet, "/?offset=40&limit=20", nil)
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

	t.Run("service error returns 500", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			return nil, errors.New("database connection lost")
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
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
					Timestamp:   now,
					CreatedAt:   now,
				}, nil
			}
			return nil, nil
		}
		ds.store.getAnnotationsFn = func(_ context.Context, _ string) (*model.EventAnnotations, error) {
			return &model.EventAnnotations{Starred: true, Alerted: false}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/events/evt-detail-001", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/events/nonexistent", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("service error returns 500", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, errors.New("disk I/O error")
		}

		req := httptest.NewRequest(http.MethodGet, "/events/evt-err", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d", rec.Code)
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
		ds.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-star-001" {
				return &model.ChangeEvent{
					ID:        "evt-star-001",
					UserName:  "alice",
					EventType: "deployment",
					Timestamp: now,
					CreatedAt: now,
				}, nil
			}
			return nil, nil
		}
		ds.store.getAnnotationsFn = func(_ context.Context, _ string) (*model.EventAnnotations, error) {
			return &model.EventAnnotations{Starred: false, Alerted: false}, nil
		}
		ds.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			return event, nil
		}
	}

	t.Run("successful toggle redirects to referer", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequest(http.MethodPost, "/events/evt-star-001/star", nil)
		req.Header.Set("Referer", "/events/evt-star-001")
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/events/evt-star-001" {
			t.Fatalf("expected Location /events/evt-star-001, got %q", loc)
		}
	})

	t.Run("no referer redirects to root", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		setupToggleStarMocks(ds)

		req := httptest.NewRequest(http.MethodPost, "/events/evt-star-001/star", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Fatalf("expected Location /, got %q", loc)
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ds := newDashboardTestStack()
		ds.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, nil
		}

		req := httptest.NewRequest(http.MethodPost, "/events/nonexistent/star", nil)
		rec := httptest.NewRecorder()
		ds.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})
}
