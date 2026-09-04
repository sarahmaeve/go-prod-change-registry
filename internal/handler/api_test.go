package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarahmaeve/go-prod-change-registry/internal/handler"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore using configurable function fields.
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

// Compile-time check that mockStore satisfies store.ChangeStore.
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

// testStack holds the components built for a single test.
type testStack struct {
	store   *mockStore
	service *service.ChangeService
	handler *handler.APIHandler
	router  chi.Router
}

// newTestStack creates a full test stack: mockStore -> ChangeService -> APIHandler,
// wired to a chi router so that chi.URLParam works correctly.
// mockPinger implements handler.Pinger for tests.
type mockPinger struct {
	err error
}

func (p *mockPinger) Ping(_ context.Context) error {
	return p.err
}

func newTestStack() *testStack {
	ms := &mockStore{}
	svc := service.NewChangeService(ms)
	h := handler.NewAPIHandler(svc, &mockPinger{})

	r := chi.NewRouter()
	r.Get("/livez", h.Liveness)
	r.Get("/readyz", h.Readiness)
	r.Get("/api/v1/health", h.HealthCheck)
	r.Post("/api/v1/events", h.CreateEvent)
	r.Get("/api/v1/events", h.ListEvents)
	r.Get("/api/v1/current", h.ListCurrent)
	r.Get("/api/v1/events/{id}", h.GetEvent)
	r.Get("/api/v1/events/{id}/annotations", h.GetEventAnnotations)
	r.Get("/api/v1/events/{id}/activity", h.GetEventActivity)
	r.Post("/api/v1/events/{id}/links", h.AddEventLinks)
	r.Post("/api/v1/events/{id}/star", h.ToggleStar)
	r.Post("/api/v1/events/{id}/alert", h.ToggleAlert)
	r.Post("/api/v1/events/{id}/close", h.CloseOperation)

	return &testStack{
		store:   ms,
		service: svc,
		handler: h,
		router:  r,
	}
}

// ---------- Health checks ----------

func TestLivenessDoesNotDependOnDatabase(t *testing.T) {
	t.Parallel()

	ms := &mockStore{}
	svc := service.NewChangeService(ms)
	h := handler.NewAPIHandler(svc, &mockPinger{err: errors.New("connection refused")})

	r := chi.NewRouter()
	r.Get("/livez", h.Liveness)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /livez status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode GET /livez response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("GET /livez status field = %q, want %q", body["status"], "ok")
	}
}

func TestReadiness(t *testing.T) {
	t.Parallel()

	t.Run("healthy database returns 200", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		for _, path := range []string{"/readyz", "/api/v1/health"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			ts.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusOK)
				continue
			}

			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Errorf("decode GET %s response: %v", path, err)
				continue
			}
			if body["status"] != "ok" {
				t.Errorf("GET %s status field = %q, want %q", path, body["status"], "ok")
			}
		}
	})

	t.Run("unreachable database returns 503", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{}
		svc := service.NewChangeService(ms)
		h := handler.NewAPIHandler(svc, &mockPinger{err: errors.New("connection refused")})

		r := chi.NewRouter()
		r.Get("/readyz", h.Readiness)
		r.Get("/api/v1/health", h.HealthCheck)

		for _, path := range []string{"/readyz", "/api/v1/health"} {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Errorf("GET %s status = %d, want %d", path, rec.Code, http.StatusServiceUnavailable)
				continue
			}

			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Errorf("decode GET %s response: %v", path, err)
				continue
			}
			if body["status"] != "unhealthy" {
				t.Errorf("GET %s status field = %q, want %q", path, body["status"], "unhealthy")
			}
		}
	})
}

// ---------- CreateEvent ----------

func TestCreateEvent(t *testing.T) {
	t.Parallel()

	t.Run("accepts ordered external links", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		ts.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			if len(event.Links) != 2 || event.Links[0].Label != "PagerDuty" || event.Links[1].URL != "https://github.com/example/repo/pull/9" {
				t.Errorf("stored links = %#v", event.Links)
			}
			return event, nil
		}
		payload := `{"user_name":"alice","event_type":"incident","links":[{"label":"PagerDuty","url":"https://example.pagerduty.com/incidents/P9"},{"label":"PR","url":"https://github.com/example/repo/pull/9"}]}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(event.Links) != 2 || event.Links[0].URL != "https://example.pagerduty.com/incidents/P9" {
			t.Errorf("response links = %#v", event.Links)
		}
	})

	t.Run("rejects non-HTTP link URLs", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		payload := `{"user_name":"alice","event_type":"incident","links":[{"label":"unsafe","url":"javascript:alert(1)"}]}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "absolute http or https") {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("valid request returns 201 with Location header", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		storedEvent := &model.ChangeEvent{
			ID:          "evt-created-001",
			UserName:    "alice",
			EventType:   "deployment",
			Description: "deploy v1.2",
			Timestamp:   now,
			CreatedAt:   now,
		}
		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return storedEvent, nil
		}

		payload := `{"user_name":"alice","event_type":"deployment","description":"deploy v1.2"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d; body: %s", rec.Code, rec.Body.String())
		}

		loc := rec.Header().Get("Location")
		if loc != "/api/v1/events/evt-created-001" {
			t.Fatalf("expected Location /api/v1/events/evt-created-001, got %s", loc)
		}

		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if event.ID != "evt-created-001" {
			t.Fatalf("expected id evt-created-001, got %q", event.ID)
		}
		if event.UserName != "alice" {
			t.Fatalf("expected user_name alice, got %q", event.UserName)
		}
		if event.EventType != "deployment" {
			t.Fatalf("expected event_type deployment, got %q", event.EventType)
		}
	})

	t.Run("missing user_name returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		payload := `{"event_type":"deployment","description":"deploy v1.2"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "validation_error" {
			t.Fatalf("expected error code validation_error, got %v", errObj["code"])
		}
	})

	t.Run("missing event_type returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		payload := `{"user_name":"alice","description":"deploy v1.2"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "validation_error" {
			t.Fatalf("expected error code validation_error, got %v", errObj["code"])
		}
	})

	t.Run("maintenance without lifecycle tags returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		payload := `{"user_name":"alice","event_type":"maintenance","description":"WAF POP2","tags":{"team":"platform"}}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "maintenance events require") {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString("{invalid"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "invalid_body" {
			t.Fatalf("expected error code invalid_body, got %v", errObj["code"])
		}
	})

	t.Run("body too large returns 413", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		// Create a valid-looking JSON body larger than 1MB.
		// The key is to have the JSON decoder read past the limit.
		largeValue := strings.Repeat("a", 1<<20)
		largeBody := `{"description":"` + largeValue + `"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(largeBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected status 413, got %d; body: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("with parent_id creates meta-event", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		parentEvent := &model.ChangeEvent{
			ID:          "evt-parent-001",
			UserName:    "alice",
			EventType:   "deployment",
			Description: "deploy v1.0",
			Timestamp:   now,
			CreatedAt:   now,
		}
		ts.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-parent-001" {
				return parentEvent, nil
			}
			return nil, nil
		}

		storedEvent := &model.ChangeEvent{
			ID:          "evt-child-001",
			ParentID:    "evt-parent-001",
			UserName:    "bob",
			EventType:   "star",
			Description: "starred",
			Timestamp:   now,
			CreatedAt:   now,
		}
		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return storedEvent, nil
		}

		payload := `{"parent_id":"evt-parent-001","user_name":"bob","event_type":"star","description":"starred"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if event.ParentID != "evt-parent-001" {
			t.Fatalf("expected parent_id evt-parent-001, got %q", event.ParentID)
		}
		if event.EventType != "star" {
			t.Fatalf("expected event_type star, got %q", event.EventType)
		}
	})

	t.Run("duplicate external_id returns 200 with existing event", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		existingEvent := &model.ChangeEvent{
			ID:          "evt-original-ext",
			ExternalID:  "gh-actions-run-555",
			UserName:    "alice",
			EventType:   "deployment",
			Description: "deploy v3.0",
			Timestamp:   now,
			CreatedAt:   now,
		}
		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return existingEvent, store.ErrDuplicate
		}

		payload := `{"external_id":"gh-actions-run-555","user_name":"bob","event_type":"deployment","description":"deploy v3.0 retry"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for duplicate external_id, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if event.ID != "evt-original-ext" {
			t.Errorf("expected original event ID %q, got %q", "evt-original-ext", event.ID)
		}
		if event.ExternalID != "gh-actions-run-555" {
			t.Errorf("expected external_id %q, got %q", "gh-actions-run-555", event.ExternalID)
		}
	})
}

// ---------- GetEvent ----------

func TestGetEvent(t *testing.T) {
	t.Parallel()

	t.Run("existing event returns 200", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		existing := &model.ChangeEvent{
			ID:          "evt-123",
			UserName:    "alice",
			EventType:   "deployment",
			Description: "deploy v2.0",
			Timestamp:   now,
			CreatedAt:   now,
		}
		ts.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-123" {
				return existing, nil
			}
			return nil, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/evt-123", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if event.ID != "evt-123" {
			t.Fatalf("expected id evt-123, got %q", event.ID)
		}
		if event.UserName != "alice" {
			t.Fatalf("expected user_name alice, got %q", event.UserName)
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/nonexistent", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "not_found" {
			t.Fatalf("expected error code not_found, got %v", errObj["code"])
		}
	})
}

// ---------- ListEvents ----------

func TestListEvents(t *testing.T) {
	t.Parallel()

	t.Run("no filters returns 200 with events array", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var result model.ListResult
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if result.Events == nil {
			t.Fatal("expected events to be non-nil slice")
		}
		if result.TotalCount != 0 {
			t.Fatalf("expected total_count 0, got %d", result.TotalCount)
		}
		if result.Limit != model.DefaultLimit {
			t.Fatalf("expected limit %d, got %d", model.DefaultLimit, result.Limit)
		}
	})

	t.Run("with time range query params", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var captured model.ListParams
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		before := time.Date(2026, 3, 31, 23, 59, 59, 0, time.UTC)
		url := "/api/v1/events?start_after=" + after.Format(time.RFC3339) + "&start_before=" + before.Format(time.RFC3339)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if captured.StartAfter == nil {
			t.Fatal("expected StartAfter to be set")
		}
		if !captured.StartAfter.Equal(after) {
			t.Fatalf("expected StartAfter %v, got %v", after, *captured.StartAfter)
		}
		if captured.StartBefore == nil {
			t.Fatal("expected StartBefore to be set")
		}
		if !captured.StartBefore.Equal(before) {
			t.Fatalf("expected StartBefore %v, got %v", before, *captured.StartBefore)
		}
	})

	t.Run("with user and type filter params", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var captured model.ListParams
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?user=alice&type=deployment", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if captured.UserName != "alice" {
			t.Fatalf("expected UserName alice, got %q", captured.UserName)
		}
		if captured.EventType != "deployment" {
			t.Fatalf("expected EventType deployment, got %q", captured.EventType)
		}
	})

	t.Run("with tag params in key:value format", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var captured model.ListParams
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?tag=env:prod&tag=region:us-east-1", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if captured.Tags == nil {
			t.Fatal("expected Tags to be set")
		}
		if captured.Tags["env"] != "prod" {
			t.Fatalf("expected tag env=prod, got %v", captured.Tags)
		}
		if captured.Tags["region"] != "us-east-1" {
			t.Fatalf("expected tag region=us-east-1, got %v", captured.Tags)
		}
	})

	t.Run("with limit and offset params", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var captured model.ListParams
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 42,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?limit=10&offset=20", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if captured.Limit != 10 {
			t.Fatalf("expected Limit 10, got %d", captured.Limit)
		}
		if captured.Offset != 20 {
			t.Fatalf("expected Offset 20, got %d", captured.Offset)
		}

		var result model.ListResult
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if result.TotalCount != 42 {
			t.Fatalf("expected total_count 42, got %d", result.TotalCount)
		}
	})

	t.Run("top_level=true is forwarded to store", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var captured model.ListParams
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events:     []model.ChangeEvent{},
				TotalCount: 0,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?top_level=true", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if !captured.TopLevel {
			t.Fatal("expected TopLevel to be true")
		}
	})

	invalidParamTests := []struct {
		name  string
		query string
	}{
		{"invalid start_after format", "start_after=not-a-date"},
		{"invalid limit not a number", "limit=abc"},
		{"malformed tag no colon", "tag=malformed"},
	}
	for _, tt := range invalidParamTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := newTestStack()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events?"+tt.query, nil)
			rec := httptest.NewRecorder()
			ts.router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400, got %d; body: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ---------- ToggleStar ----------

func TestToggleStar(t *testing.T) {
	t.Parallel()

	t.Run("creates star meta-event and returns 201", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var createdEvent *model.ChangeEvent
		ts.store.toggleStarFn = func(_ context.Context, eventID, userName string) (*model.ChangeEvent, error) {
			createdEvent = &model.ChangeEvent{
				ID:        "star-1",
				ParentID:  eventID,
				UserName:  userName,
				EventType: model.EventTypeStar,
			}
			return createdEvent, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events/evt-parent-star/star", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("expected status 201, got %d; body: %s", rec.Code, rec.Body.String())
		}

		if createdEvent == nil {
			t.Fatal("expected store.ToggleStar to be called")
		}
		if createdEvent.ParentID != "evt-parent-star" {
			t.Fatalf("expected parent_id evt-parent-star, got %q", createdEvent.ParentID)
		}
		if createdEvent.EventType != model.EventTypeStar {
			t.Fatalf("expected event_type star, got %q", createdEvent.EventType)
		}
	})

	t.Run("non-existent parent returns 404", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.toggleStarFn = func(_ context.Context, _, _ string) (*model.ChangeEvent, error) {
			return nil, store.ErrNotFound
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events/nonexistent/star", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "not_found" {
			t.Fatalf("expected error code not_found, got %v", errObj["code"])
		}
	})
}

func TestEventActionEndpoints(t *testing.T) {
	t.Parallel()

	t.Run("append links", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "event-1"}, nil
		}
		ts.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			return event, nil
		}
		body := `{"user_name":"alice","links":[{"label":"Plan","url":"https://notion.so/example/plan"},{"label":"PR","url":"https://github.com/example/repo/pull/1"}]}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events/event-1/links", strings.NewReader(body))
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if event.ParentID != "event-1" || event.EventType != model.EventTypeLink || len(event.Links) != 2 {
			t.Errorf("link annotation = %+v", event)
		}
	})

	t.Run("list activity", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "event-1"}, nil
		}
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{Events: []model.ChangeEvent{{ID: "link-1", ParentID: params.ParentID}}}, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/event-1/activity", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"link-1"`) {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("toggle alert", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		ts.store.toggleAlertFn = func(_ context.Context, eventID, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "alert-1", ParentID: eventID, EventType: model.EventTypeAlert}, nil
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events/event-1/alert", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"event_type":"alert"`) {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("close operation", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		start := &model.ChangeEvent{ID: "start-1", EventType: "deployment", Tags: map[string]string{"phase": "start", "change_id": "change-1"}}
		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) { return start, nil }
		ts.store.listCurrentFn = func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
			return &model.ListResult{TotalCount: 1}, nil
		}
		ts.store.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) { return event, nil }
		body := `{"user_name":"alice","description":"rollout complete"}`
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/events/start-1/close", strings.NewReader(body))
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"phase":"end"`) {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})
}

// ---------- ListCurrent ----------

func TestListCurrent(t *testing.T) {
	t.Parallel()

	t.Run("returns current events and forwards filters", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		var captured model.CurrentParams
		ts.store.listCurrentFn = func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			captured = params
			return &model.ListResult{
				Events: []model.ChangeEvent{{
					ID:        "active-event",
					EventType: "deployment",
					Tags:      map[string]string{"phase": "start", "change_id": "change-1"},
				}},
				TotalCount: 1,
				Limit:      25,
				Offset:     5,
			}, nil
		}

		req := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodGet,
			"/api/v1/current?for_team=payments&scope=service&scope=site&severity=sev0&severity=sev1&type=deployment&limit=25&offset=5",
			nil,
		)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
		}
		if captured.ForTeam != "payments" || captured.EventType != "deployment" || captured.Limit != 25 || captured.Offset != 5 {
			t.Errorf("captured params = %+v", captured)
		}
		if len(captured.Scopes) != 2 || len(captured.Severities) != 2 {
			t.Errorf("captured repeated params = %+v", captured)
		}

		var result model.ListResult
		if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result.TotalCount != 1 || len(result.Events) != 1 || result.Events[0].ID != "active-event" {
			t.Errorf("result = %+v", result)
		}
	})

	t.Run("rejects malformed pagination", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current?limit=nope", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("returns 500 on service failure", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()
		ts.store.listCurrentFn = func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
			return nil, errors.New("database unavailable")
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/current", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500; body = %s", rec.Code, rec.Body.String())
		}
	})
}

// ---------- GetEventAnnotations ----------

func TestGetEventAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("returns annotation state", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.getAnnotationsFn = func(_ context.Context, eventID string) (*model.EventAnnotations, error) {
			if eventID == "evt-ann-001" {
				return &model.EventAnnotations{Starred: true, Alerted: false}, nil
			}
			return nil, nil
		}

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/events/evt-ann-001/annotations", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var annotations model.EventAnnotations
		if err := json.NewDecoder(rec.Body).Decode(&annotations); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if !annotations.Starred {
			t.Fatal("expected starred to be true")
		}
		if annotations.Alerted {
			t.Fatal("expected alerted to be false")
		}
	})
}
