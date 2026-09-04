package router_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/config"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/humanauth"
	"github.com/sarahmaeve/go-prod-change-registry/internal/middleware"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/router"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore with configurable function fields.
type mockStore struct {
	createFn              func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	toggleStarFn          func(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error)
	toggleAlertFn         func(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error)
	getByIDFn             func(ctx context.Context, id string) (*model.ChangeEvent, error)
	getByExternalIDFn     func(ctx context.Context, externalID string) (*model.ChangeEvent, error)
	listFn                func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	listCurrentFn         func(ctx context.Context, params model.CurrentParams) (*model.ListResult, error)
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

func (m *mockStore) ToggleStar(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	if m.toggleStarFn != nil {
		return m.toggleStarFn(ctx, eventID, user.Name)
	}
	panic("unexpected call to ToggleStar")
}

func (m *mockStore) ToggleAlert(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	if m.toggleAlertFn != nil {
		return m.toggleAlertFn(ctx, eventID, user.Name)
	}
	panic("unexpected call to ToggleAlert")
}

func (m *mockStore) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	panic("unexpected call to GetByID")
}

func (m *mockStore) GetByExternalID(ctx context.Context, externalID string) (*model.ChangeEvent, error) {
	if m.getByExternalIDFn != nil {
		return m.getByExternalIDFn(ctx, externalID)
	}
	return nil, nil
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn != nil {
		return m.listFn(ctx, params)
	}
	panic("unexpected call to List")
}

func (m *mockStore) ListCurrent(ctx context.Context, params model.CurrentParams) (*model.ListResult, error) {
	if m.listCurrentFn != nil {
		return m.listCurrentFn(ctx, params)
	}
	panic("unexpected call to ListCurrent")
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

func (p *mockPinger) Ping(_ context.Context) error { return nil }

const testToken = "test-secret-token"

// newTestRouter creates a full router with auth middleware and mock store.
func newTestRouter(t *testing.T, requireAuthReads bool) (http.Handler, *mockStore) {
	t.Helper()
	authenticator := humanauth.NewGitHub(humanauth.ProviderOptions{ClientID: "test", AllowAny: true})
	humanAuthH := handler.NewHumanAuthHandler(authenticator, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	return newTestRouterWithHumanAuth(t, requireAuthReads, humanAuthH, "github")
}

func newTestRouterWithHumanAuth(t *testing.T, requireAuthReads bool, humanAuthH *handler.HumanAuthHandler, provider string) (http.Handler, *mockStore) {
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
		listCurrentFn: func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
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
		toggleStarFn: func(_ context.Context, eventID, userName string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{
				ID:        "star-id",
				ParentID:  eventID,
				UserName:  userName,
				EventType: model.EventTypeStar,
				Timestamp: now,
				CreatedAt: now,
			}, nil
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

	cfg := &config.Config{
		APITokens:         []string{testToken},
		RequireAuthReads:  requireAuthReads,
		SessionSecret:     []byte("test-session-secret"),
		HumanAuthProvider: provider,
	}

	r := router.New(apiH, dashH, humanAuthH, cfg)
	return r, ms
}

func TestBeyondLogoutDoesNotRequireCurrentGroupMembership(t *testing.T) {
	t.Parallel()

	beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
	humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	r, _ := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")

	cookieRecorder := httptest.NewRecorder()
	if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
		Secret: []byte("test-session-secret"), Duration: time.Hour,
	}, humanauth.Principal{Provider: "beyond", Subject: "alice@example.com", UserName: "alice@example.com"}); err != nil {
		t.Fatalf("SetHumanSessionCookie(): %v", err)
	}
	cookie := cookieRecorder.Result().Cookies()[0]
	sessionRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	sessionRequest.AddCookie(cookie)
	session, err := middleware.ReadHumanSession(sessionRequest, []byte("test-session-secret"), "beyond")
	if err != nil {
		t.Fatalf("ReadHumanSession(): %v", err)
	}

	form := url.Values{"csrf_token": {middleware.GenerateCSRFToken([]byte("test-session-secret"), session.Nonce)}}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/logout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /logout status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("POST /logout cookies = %#v, want one deletion cookie", cookies)
	}
}

func TestBeyondIdentityAuthenticatesAPIAndControlsAttribution(t *testing.T) {
	t.Parallel()

	beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
	humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	r, ms := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")

	var captured *model.ChangeEvent
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		copy := *event
		captured = &copy
		return &copy, nil
	}

	body := `{"user_name":"forged-actor","event_type":"deployment","description":"deploy v1.3"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beyond-Email", "Alice@Example.com")
	req.Header.Set("X-Beyond-Name", "Alice Example")
	req.Header.Set("X-Beyond-Groups", "engineering|team-all")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("store did not receive an event")
	}
	if captured.UserName != "alice@example.com" || captured.UserProvider != "beyond" || captured.UserSubject != "alice@example.com" {
		t.Errorf("stored identity = %q/%q/%q, want verified Beyond identity", captured.UserName, captured.UserProvider, captured.UserSubject)
	}
}

func TestBeyondIdentityMakesAPIUserNameOptional(t *testing.T) {
	t.Parallel()

	beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
	humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	r, ms := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")

	var captured *model.ChangeEvent
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		copy := *event
		captured = &copy
		return &copy, nil
	}

	body := `{"external_id":"build-123","event_type":"deployment","description":"deploy v1.3"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beyond-Email", "alice@example.com")
	req.Header.Set("X-Beyond-Groups", "engineering")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
	if captured == nil || captured.UserName != "alice@example.com" {
		t.Fatalf("stored event = %+v, want identity-derived user name", captured)
	}
}

func TestBeyondIdentityAPIRejectsWrongGroup(t *testing.T) {
	t.Parallel()

	beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
	humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	r, ms := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")

	storeCalled := false
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		storeCalled = true
		return event, nil
	}

	body := `{"event_type":"deployment","description":"deploy v1.3"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Beyond-Email", "alice@example.com")
	req.Header.Set("X-Beyond-Groups", "finance")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body = %s", rec.Code, rec.Body.String())
	}
	if storeCalled {
		t.Fatal("store was called for a disallowed Beyond identity")
	}
}

func TestBeyondAPIRetainsLegacyTokenDuringMigration(t *testing.T) {
	t.Parallel()

	beyond := humanauth.NewBeyond(humanauth.ProviderOptions{AllowedOrgs: []string{"engineering"}})
	humanAuthH := handler.NewBeyondHumanAuthHandler(beyond, handler.HumanAuthOptions{
		SessionSecret: []byte("test-session-secret"), SessionDuration: time.Hour,
	})
	r, _ := newTestRouterWithHumanAuth(t, true, humanAuthH, "beyond")

	body := `{"user_name":"legacy-producer","event_type":"deployment","description":"deploy v1.3"}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Token "+testToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rec.Code, rec.Body.String())
	}
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
			status int
		}{
			{
				name:   "POST /api/v1/events without auth",
				method: http.MethodPost,
				path:   "/api/v1/events",
				body:   `{"user_name":"sarah","event_type":"deployment","description":"test"}`,
				status: http.StatusUnauthorized,
			},
			{
				name:   "GET /api/v1/events without auth",
				method: http.MethodGet,
				path:   "/api/v1/events",
				status: http.StatusUnauthorized,
			},
			{
				name:   "GET /api/v1/current without auth",
				method: http.MethodGet,
				path:   "/api/v1/current",
				status: http.StatusUnauthorized,
			},
			{
				name:   "GET /api/v1/events/{id} without auth",
				method: http.MethodGet,
				path:   "/api/v1/events/some-id",
				status: http.StatusUnauthorized,
			},
			{
				name:   "GET /api/v1/events/{id}/annotations without auth",
				method: http.MethodGet,
				path:   "/api/v1/events/some-id/annotations",
				status: http.StatusUnauthorized,
			},
			{
				name:   "POST /api/v1/events/{id}/star without auth",
				method: http.MethodPost,
				path:   "/api/v1/events/some-id/star",
				status: http.StatusUnauthorized,
			},
			{
				name:   "GET / (dashboard) without auth",
				method: http.MethodGet,
				path:   "/",
				status: http.StatusFound,
			},
			{
				name:   "GET /events/{id} (detail) without auth",
				method: http.MethodGet,
				path:   "/events/some-id",
				status: http.StatusFound,
			},
			{
				name:   "GET /events/new without auth",
				method: http.MethodGet,
				path:   "/events/new",
				status: http.StatusFound,
			},
			{
				name:   "POST /events without auth",
				method: http.MethodPost,
				path:   "/events",
				status: http.StatusUnauthorized,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				var req *http.Request
				if tc.body != "" {
					req = httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, bytes.NewBufferString(tc.body))
					req.Header.Set("Content-Type", "application/json")
				} else {
					req = httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
				}

				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				if rec.Code != tc.status {
					t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, rec.Code, tc.status)
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

				req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
				req.Header.Set("Authorization", "Bearer "+testToken)
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, req)

				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("expected 405 for %s %s, got %d", tc.method, tc.path, rec.Code)
				}
			})
		}
	})

	t.Run("health endpoints are accessible without auth", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		for _, path := range []string{"/livez", "/readyz", "/api/v1/health"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusOK)
			}
		}
	})

	t.Run("Bearer token allows creating events", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		body := `{"user_name":"sarah","event_type":"deployment","description":"deploy v1.3"}`
		req := httptest.NewRequestWithContext(
			t.Context(),
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

	t.Run("session cookie is limited to dashboard routes", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		cookieRecorder := httptest.NewRecorder()
		if err := middleware.SetHumanSessionCookie(cookieRecorder, middleware.HumanSessionOptions{
			Secret: []byte("test-session-secret"), Duration: time.Hour,
		}, humanauth.Principal{Provider: "github", Subject: "12345", UserName: "alice"}); err != nil {
			t.Fatalf("SetHumanSessionCookie(): %v", err)
		}
		cookies := cookieRecorder.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("session cookie count = %d, want 1", len(cookies))
		}

		apiReq := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			"/api/v1/events",
			bytes.NewBufferString(`{"user_name":"sarah","event_type":"deployment"}`),
		)
		apiReq.Header.Set("Content-Type", "application/json")
		apiReq.AddCookie(cookies[0])
		apiRec := httptest.NewRecorder()
		r.ServeHTTP(apiRec, apiReq)
		if apiRec.Code != http.StatusUnauthorized {
			t.Fatalf("API status = %d, want 401", apiRec.Code)
		}

		dashboardReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		dashboardReq.AddCookie(cookies[0])
		dashboardRec := httptest.NewRecorder()
		r.ServeHTTP(dashboardRec, dashboardReq)
		if dashboardRec.Code != http.StatusOK {
			t.Fatalf("dashboard status = %d, want 200", dashboardRec.Code)
		}
	})

	t.Run("query param token does not authenticate dashboard", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/?token="+testToken, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("dashboard status = %d, want login redirect", rec.Code)
		}
	})

	t.Run("query param token allows listing events", func(t *testing.T) {
		t.Parallel()

		r, _ := newTestRouter(t, true)
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?token="+testToken, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	})

	t.Run("current endpoint follows read authentication setting", func(t *testing.T) {
		t.Parallel()

		requiredRouter, _ := newTestRouter(t, true)
		authenticated := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current?token="+testToken, nil)
		authenticatedRec := httptest.NewRecorder()
		requiredRouter.ServeHTTP(authenticatedRec, authenticated)
		if authenticatedRec.Code != http.StatusOK {
			t.Errorf("authenticated status = %d, want 200", authenticatedRec.Code)
		}

		optionalRouter, _ := newTestRouter(t, false)
		unauthenticated := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current", nil)
		unauthenticatedRec := httptest.NewRecorder()
		optionalRouter.ServeHTTP(unauthenticatedRec, unauthenticated)
		if unauthenticatedRec.Code != http.StatusOK {
			t.Errorf("optional-auth status = %d, want 200", unauthenticatedRec.Code)
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

				req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
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
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(body))
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
		req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil)
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
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", nil)
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
