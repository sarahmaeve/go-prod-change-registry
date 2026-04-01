package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sarah/go-prod-change-registry/internal/handler"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/service"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore using configurable function fields.
type mockStore struct {
	createFn  func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	getByIDFn func(ctx context.Context, id string) (*model.ChangeEvent, error)
	updateFn  func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	deleteFn  func(ctx context.Context, id string) error
	listFn    func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
}

// Compile-time check that mockStore satisfies store.ChangeStore.
var _ store.ChangeStore = (*mockStore)(nil)

func (m *mockStore) Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.createFn != nil {
		return m.createFn(ctx, event)
	}
	panic("unexpected call to CreateFn")
}

func (m *mockStore) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, id)
	}
	panic("unexpected call to GetByIDFn")
}

func (m *mockStore) Update(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, event)
	}
	panic("unexpected call to UpdateFn")
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	panic("unexpected call to DeleteFn")
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn != nil {
		return m.listFn(ctx, params)
	}
	panic("unexpected call to ListFn")
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
func newTestStack() *testStack {
	ms := &mockStore{}
	svc := service.NewChangeService(ms)
	h := handler.NewAPIHandler(svc)

	r := chi.NewRouter()
	r.Get("/api/v1/health", h.HealthCheck)
	r.Route("/api/v1/events", func(r chi.Router) {
		r.Get("/", h.ListEvents)
		r.Post("/", h.CreateEvent)
		r.Get("/{id}", h.GetEvent)
		r.Patch("/{id}", h.UpdateEvent)
		r.Put("/{id}", h.UpdateEvent)
		r.Delete("/{id}", h.DeleteEvent)
	})

	return &testStack{
		store:   ms,
		service: svc,
		handler: h,
		router:  r,
	}
}

// ---------- HealthCheck ----------

func TestHealthCheck(t *testing.T) {
	t.Parallel()

	ts := newTestStack()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	ts.router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", body["status"])
	}
}

// ---------- CreateEvent ----------

func TestCreateEvent(t *testing.T) {
	t.Parallel()

	t.Run("valid request returns 201 with Location header and event JSON", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		fixedTime := time.Date(2026, 3, 15, 10, 0, 0, 0, time.UTC)
		storedEvent := &model.ChangeEvent{
			ID:             "evt-created-001",
			UserName:       "alice",
			EventType:      "deployment",
			Description:    "deploy v1.2",
			TimestampStart: fixedTime,
			CreatedAt:      fixedTime,
			UpdatedAt:      fixedTime,
		}
		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return storedEvent, nil
		}

		payload := `{"user_name":"alice","event_type":"deployment","description":"deploy v1.2"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/", bytes.NewBufferString(payload))
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
		if event.Description != "deploy v1.2" {
			t.Fatalf("expected description 'deploy v1.2', got %q", event.Description)
		}
		if !event.CreatedAt.Equal(fixedTime) {
			t.Fatalf("expected created_at %v, got %v", fixedTime, event.CreatedAt)
		}
	})

	t.Run("missing user_name returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		payload := `{"event_type":"deployment","description":"deploy v1.2"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/", bytes.NewBufferString(payload))
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

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/", bytes.NewBufferString("{invalid"))
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

	t.Run("response body contains the created event", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		fixedTime := time.Date(2026, 2, 20, 14, 30, 0, 0, time.UTC)
		storedEvent := &model.ChangeEvent{
			ID:             "evt-created-002",
			UserName:       "bob",
			EventType:      "feature-flag",
			Description:    "enable dark mode",
			TimestampStart: fixedTime.Add(-1 * time.Hour),
			Tags:           map[string]string{"env": "prod", "region": "us-east-1"},
			CreatedAt:      fixedTime,
			UpdatedAt:      fixedTime,
		}
		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return storedEvent, nil
		}

		tsStart := fixedTime.Add(-1 * time.Hour)
		payload, _ := json.Marshal(model.CreateChangeRequest{
			UserName:       "bob",
			EventType:      "feature-flag",
			Description:    "enable dark mode",
			TimestampStart: &tsStart,
			Tags:           map[string]string{"env": "prod", "region": "us-east-1"},
		})

		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/", bytes.NewBuffer(payload))
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
		if event.ID != "evt-created-002" {
			t.Fatalf("expected id evt-created-002, got %q", event.ID)
		}
		if event.UserName != "bob" {
			t.Fatalf("expected user_name bob, got %q", event.UserName)
		}
		if event.Tags["env"] != "prod" {
			t.Fatalf("expected tag env=prod, got %v", event.Tags)
		}
		if event.Tags["region"] != "us-east-1" {
			t.Fatalf("expected tag region=us-east-1, got %v", event.Tags)
		}
	})

	t.Run("store_error_returns_500", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.createFn = func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
			return nil, errors.New("database connection lost")
		}

		payload := `{"user_name":"alice","event_type":"deployment","description":"deploy v1.2"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/events/", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "internal_error" {
			t.Fatalf("expected error code internal_error, got %v", errObj["code"])
		}
	})
}

// ---------- GetEvent ----------

func TestGetEvent(t *testing.T) {
	t.Parallel()

	t.Run("existing event returns 200 with event JSON", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		existing := &model.ChangeEvent{
			ID:             "evt-123",
			UserName:       "alice",
			EventType:      "deployment",
			Description:    "deploy v2.0",
			TimestampStart: time.Now().UTC(),
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}
		ts.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-123" {
				return existing, nil
			}
			return nil, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/evt-123", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/nonexistent", nil)
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

	t.Run("mock returning nil triggers not-found", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/abc", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", rec.Code)
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

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/", nil)
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
		if result.Offset != 0 {
			t.Fatalf("expected offset 0, got %d", result.Offset)
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
		url := "/api/v1/events/?start_after=" + after.Format(time.RFC3339) + "&start_before=" + before.Format(time.RFC3339)

		req := httptest.NewRequest(http.MethodGet, url, nil)
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

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/?user=alice&type=deployment", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/?tag=env:prod&tag=region:us-east-1", nil)
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

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/?limit=10&offset=20", nil)
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
		if result.Limit != 10 {
			t.Fatalf("expected limit 10, got %d", result.Limit)
		}
		if result.Offset != 20 {
			t.Fatalf("expected offset 20, got %d", result.Offset)
		}
	})

	t.Run("response structure matches expected shape", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		ts.store.listFn = func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			return &model.ListResult{
				Events: []model.ChangeEvent{
					{
						ID:             "evt-1",
						UserName:       "alice",
						EventType:      "deployment",
						Description:    "deploy v1",
						TimestampStart: now,
						CreatedAt:      now,
						UpdatedAt:      now,
					},
				},
				TotalCount: 1,
				Limit:      params.Limit,
				Offset:     params.Offset,
			}, nil
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		// Verify the raw JSON structure has the expected top-level keys.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		for _, key := range []string{"events", "total_count", "limit", "offset"} {
			if _, ok := raw[key]; !ok {
				t.Fatalf("expected key %q in response JSON", key)
			}
		}
	})

	t.Run("store_error_returns_500", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.listFn = func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
			return nil, errors.New("database timeout")
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/events/", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected status 500, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var body map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatal("expected error object in response")
		}
		if errObj["code"] != "internal_error" {
			t.Fatalf("expected error code internal_error, got %v", errObj["code"])
		}
	})

	invalidParamTests := []struct {
		name  string
		query string
	}{
		{"invalid_start_after_format", "start_after=not-a-date"},
		{"invalid_limit_not_a_number", "limit=abc"},
		{"malformed_tag_no_colon", "tag=malformed"},
	}
	for _, tt := range invalidParamTests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := newTestStack()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/events/?"+tt.query, nil)
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
			if errObj["code"] != "invalid_parameter" {
				t.Fatalf("expected error code invalid_parameter, got %v", errObj["code"])
			}
		})
	}
}

// ---------- UpdateEvent ----------

func TestUpdateEvent(t *testing.T) {
	t.Parallel()

	t.Run("valid partial update returns 200", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		now := time.Now().UTC()
		existing := &model.ChangeEvent{
			ID:             "evt-456",
			UserName:       "alice",
			EventType:      "deployment",
			Description:    "deploy v1.0",
			TimestampStart: now,
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		ts.store.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id == "evt-456" {
				// Return a copy to avoid mutation issues across calls.
				copy := *existing
				return &copy, nil
			}
			return nil, nil
		}
		ts.store.updateFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			return event, nil
		}

		newDesc := "deploy v2.0"
		payload, _ := json.Marshal(model.UpdateChangeRequest{
			Description: &newDesc,
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/events/evt-456", bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d; body: %s", rec.Code, rec.Body.String())
		}

		var event model.ChangeEvent
		if err := json.NewDecoder(rec.Body).Decode(&event); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}
		if event.Description != "deploy v2.0" {
			t.Fatalf("expected description 'deploy v2.0', got %q", event.Description)
		}
		if event.UserName != "alice" {
			t.Fatalf("expected user_name alice (unchanged), got %q", event.UserName)
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.getByIDFn = func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return nil, nil
		}

		newDesc := "updated"
		payload, _ := json.Marshal(model.UpdateChangeRequest{
			Description: &newDesc,
		})

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/events/nonexistent", bytes.NewBuffer(payload))
		req.Header.Set("Content-Type", "application/json")
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

// ---------- DeleteEvent ----------

func TestDeleteEvent(t *testing.T) {
	t.Parallel()

	t.Run("successful delete returns 204 with empty body", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		var capturedID string
		ts.store.deleteFn = func(_ context.Context, id string) error {
			capturedID = id
			return nil
		}

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/events/evt-789", nil)
		rec := httptest.NewRecorder()
		ts.router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected status 204, got %d; body: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("expected empty body, got %q", rec.Body.String())
		}
		if capturedID != "evt-789" {
			t.Fatalf("expected delete called with id evt-789, got %q", capturedID)
		}
	})

	t.Run("non-existent event returns 404", func(t *testing.T) {
		t.Parallel()

		ts := newTestStack()

		ts.store.deleteFn = func(_ context.Context, id string) error {
			return service.ErrEventNotFound
		}

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/events/nonexistent", nil)
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
