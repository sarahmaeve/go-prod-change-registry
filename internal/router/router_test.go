package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/config"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/middleware"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/router"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore with configurable function fields.
type mockStore struct {
	createFn              func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	getByIDFn             func(ctx context.Context, id string) (*model.ChangeEvent, error)
	listFn                func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	getAnnotationsFn      func(ctx context.Context, eventID string) (*model.EventAnnotations, error)
	getAnnotationsBatchFn func(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error)
}

var _ store.ChangeStore = (*mockStore)(nil)

func (m *mockStore) Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.createFn != nil {
		return m.createFn(ctx, event)
	}
	panic("unexpected call to Create")
}

func (m *mockStore) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	panic("unexpected call to GetByID")
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn != nil {
		return m.listFn(ctx, params)
	}
	panic("unexpected call to List")
}

func (m *mockStore) GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error) {
	if m.getAnnotationsFn != nil {
		return m.getAnnotationsFn(ctx, eventID)
	}
	panic("unexpected call to GetAnnotations")
}

func (m *mockStore) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error) {
	if m.getAnnotationsBatchFn != nil {
		return m.getAnnotationsBatchFn(ctx, eventIDs)
	}
	panic("unexpected call to GetAnnotationsBatch")
}

func (m *mockStore) Close() error { return nil }

type mockPinger struct{}

func (p *mockPinger) PingContext(_ context.Context) error { return nil }

const testToken = "test-secret-token"

// newTestRouter creates a full router with auth middleware and mock store.
func newTestRouter(t *testing.T, requireAuthReads bool) (http.Handler, *mockStore) {
	t.Helper()

	now := time.Now().UTC()
	ms := &mockStore{
		listFn: func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      50,
				Offset:     0,
			}, nil
		},
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			cp := *event
			return &cp, nil
		},
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{
				ID:          "some-id",
				UserName:    "test",
				EventType:   "deployment",
				Description: "test event",
				Timestamp:   now,
				CreatedAt:   now,
			}, nil
		},
		getAnnotationsFn: func(_ context.Context, _ string) (*model.EventAnnotations, error) {
			return &model.EventAnnotations{Starred: false, Alerted: false}, nil
		},
		getAnnotationsBatchFn: func(_ context.Context, _ []string) (map[string]*model.EventAnnotations, error) {
			return map[string]*model.EventAnnotations{}, nil
		},
	}

	svc := service.NewChangeService(ms)
	apiH := handler.NewAPIHandler(svc, &mockPinger{})
	dashH := handler.NewDashboardHandler(svc, 0, []byte("test-session-secret"))
	loginH := handler.NewLoginHandler([]string{testToken}, middleware.SessionOptions{Secret: []byte("test-session-secret")})

	cfg := &config.Config{
		APITokens:        []string{testToken},
		RequireAuthReads: requireAuthReads,
		SessionSecret:    []byte("test-session-secret"),
	}

	r := router.New(apiH, dashH, loginH, cfg)
	return r, ms
}

func TestAuthEnforcement(t *testing.T) {
	t.Parallel()

	t.Run("unauthenticated requests are blocked", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)

		tests := []struct {
			name   string
			method string
			path   string
			body   string
		}{
			{
				name:   "POST /api/v1/events without auth",
				method: http.MethodPost,
				path:   "/api/v1/events",
				body:   `{"user_name":"sarah","event_type":"deployment","description":"test"}`,
			},
			{
				name:   "GET /api/v1/events without auth",
				method: http.MethodGet,
				path:   "/api/v1/events",
			},
			{
				name:   "GET /api/v1/events/{id} without auth",
				method: http.MethodGet,
				path:   "/api/v1/events/some-id",
			},
			{
				name:   "GET /api/v1/events/{id}/annotations without auth",
				method: http.MethodGet,
				path:   "/api/v1/events/some-id/annotations",
			},
			{
				name:   "POST /api/v1/events/{id}/star without auth",
				method: http.MethodPost,
				path:   "/api/v1/events/some-id/star",
			},
			{
				name:   "GET / (dashboard) without auth",
				method: http.MethodGet,
				path:   "/",
			},
			{
				name:   "GET /events/{id} (detail) without auth",
				method: http.MethodGet,
				path:   "/events/some-id",
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				var req *http.Request
				if tc.body != "" {
					req = httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.body))
					req.Header.Set("Content-Type", "application/json")
				} else {
					req = httptest.NewRequest(tc.method, tc.path, nil)
				}

				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("expected 401 for %s %s, got %d", tc.method, tc.path, rec.Code)
				}
			})
		}
	})

	t.Run("no PUT or DELETE routes return 405", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)

		tests := []struct {
			name   string
			method string
			path   string
		}{
			{"PUT /api/v1/events/{id}", http.MethodPut, "/api/v1/events/some-id"},
			{"DELETE /api/v1/events/{id}", http.MethodDelete, "/api/v1/events/some-id"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				req := httptest.NewRequest(tc.method, tc.path, nil)
				req.Header.Set("Authorization", "Bearer "+testToken)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("expected 405 for %s %s, got %d", tc.method, tc.path, rec.Code)
				}
			})
		}
	})

	t.Run("health endpoint is accessible without auth", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for health, got %d", rec.Code)
		}
	})

	t.Run("Bearer token allows creating events", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		body := `{"user_name":"sarah","event_type":"deployment","description":"deploy v1.3"}`
		req := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/events",
			bytes.NewBufferString(body),
		)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testToken)

		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("query param token allows viewing dashboard", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequest(http.MethodGet, "/?token="+testToken, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for dashboard with token param, got %d", rec.Code)
		}
	})

	t.Run("query param token allows listing events", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/events?token="+testToken, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("invalid token is rejected", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)

		tests := []struct {
			name   string
			method string
			path   string
			token  string
		}{
			{"invalid query param token", http.MethodGet, "/api/v1/events?token=wrong", ""},
			{"invalid bearer token", http.MethodPost, "/api/v1/events", "wrong-token"},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				req := httptest.NewRequest(tc.method, tc.path, nil)
				if tc.token != "" {
					req.Header.Set("Authorization", "Bearer "+tc.token)
				}
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("expected 401, got %d", rec.Code)
				}
			})
		}
	})

	t.Run("store not called on auth failure", func(t *testing.T) {
		t.Parallel()

		r, ms := newTestRouter(t, true)

		storeCalled := false
		ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			storeCalled = true
			cp := *event
			return &cp, nil
		}
		ms.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			storeCalled = true
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      50,
				Offset:     0,
			}, nil
		}

		// Try a POST without auth.
		body := `{"user_name":"sarah","event_type":"deployment","description":"sneaky deploy"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer wrong-token")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if storeCalled {
			t.Fatal("store was called despite invalid auth")
		}

		// Try a GET without auth.
		req = httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
		rec = httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if storeCalled {
			t.Fatal("store was called despite missing auth")
		}
	})

	t.Run("401 response is JSON with error structure", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}

		ct := rec.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Fatalf("expected Content-Type application/json, got %q", ct)
		}

		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode JSON body: %v", err)
		}
		if body.Error.Code != "unauthorized" {
			t.Fatalf("expected error code %q, got %q", "unauthorized", body.Error.Code)
		}
	})
}
