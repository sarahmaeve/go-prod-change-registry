package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore using function fields so each test can
// customise behaviour. Any method called without its function field set panics,
// catching unexpected calls early.
type mockStore struct {
	createFn              func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	getByIDFn             func(ctx context.Context, id string) (*model.ChangeEvent, error)
	listFn                func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	getAnnotationsFn      func(ctx context.Context, eventID string) (*model.EventAnnotations, error)
	getAnnotationsBatchFn func(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error)
}

func (m *mockStore) Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.createFn == nil {
		panic("unexpected call to Create")
	}
	return m.createFn(ctx, event)
}

func (m *mockStore) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	if m.getByIDFn == nil {
		panic("unexpected call to GetByID")
	}
	return m.getByIDFn(ctx, id)
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn == nil {
		panic("unexpected call to List")
	}
	return m.listFn(ctx, params)
}

func (m *mockStore) GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error) {
	if m.getAnnotationsFn == nil {
		panic("unexpected call to GetAnnotations")
	}
	return m.getAnnotationsFn(ctx, eventID)
}

func (m *mockStore) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error) {
	if m.getAnnotationsBatchFn == nil {
		panic("unexpected call to GetAnnotationsBatch")
	}
	return m.getAnnotationsBatchFn(ctx, eventIDs)
}

func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func timePtr(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("successful with all fields", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()

		var captured *model.ChangeEvent
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				captured = event
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		req := &model.CreateChangeRequest{
			UserName:        "alice",
			EventType:       model.EventTypeDeployment,
			Description:     "deploy v42",
			LongDescription: "full rollout of v42",
			Tags:            map[string]string{"env": "prod"},
		}

		got, err := svc.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after := time.Now().UTC()

		// UUID must be set and non-empty.
		if got.ID == "" {
			t.Fatal("expected non-empty ID")
		}

		// CreatedAt should be within [before, after].
		if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
			t.Fatalf("CreatedAt %v not in [%v, %v]", got.CreatedAt, before, after)
		}

		// Timestamp should default to ~now when not set.
		if got.Timestamp.Before(before) || got.Timestamp.After(after) {
			t.Fatalf("Timestamp %v not in [%v, %v]", got.Timestamp, before, after)
		}

		// Verify remaining fields.
		if got.UserName != "alice" {
			t.Errorf("UserName = %q, want %q", got.UserName, "alice")
		}
		if got.EventType != model.EventTypeDeployment {
			t.Errorf("EventType = %q, want %q", got.EventType, model.EventTypeDeployment)
		}
		if got.Description != "deploy v42" {
			t.Errorf("Description = %q, want %q", got.Description, "deploy v42")
		}
		if got.LongDescription != "full rollout of v42" {
			t.Errorf("LongDescription = %q, want %q", got.LongDescription, "full rollout of v42")
		}
		if got.Tags["env"] != "prod" {
			t.Errorf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
		}
		if got.ParentID != "" {
			t.Errorf("ParentID = %q, want empty", got.ParentID)
		}

		if captured == nil {
			t.Fatal("store.Create was not called")
		}
	})

	t.Run("missing user_name errors", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{} // no createFn -- store should not be called
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			EventType:   model.EventTypeDeployment,
			Description: "oops",
		})
		if !errors.Is(err, ErrUserNameRequired) {
			t.Fatalf("got error %v, want %v", err, ErrUserNameRequired)
		}
	})

	t.Run("missing event_type errors", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{} // no createFn -- store should not be called
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:    "alice",
			Description: "oops",
		})
		if !errors.Is(err, ErrEventTypeRequired) {
			t.Fatalf("got error %v, want %v", err, ErrEventTypeRequired)
		}
	})

	t.Run("with parent_id verifies parent existence", func(t *testing.T) {
		t.Parallel()

		parentEvt := &model.ChangeEvent{
			ID:       "parent-1",
			UserName: "alice",
		}

		var capturedEvent *model.ChangeEvent
		ms := &mockStore{
			getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
				if id != "parent-1" {
					t.Errorf("GetByID called with %q, want %q", id, "parent-1")
				}
				return parentEvt, nil
			},
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				capturedEvent = event
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			ParentID:  "parent-1",
			UserName:  "bob",
			EventType: model.EventTypeStar,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ParentID != "parent-1" {
			t.Errorf("ParentID = %q, want %q", got.ParentID, "parent-1")
		}
		if capturedEvent == nil {
			t.Fatal("store.Create was not called")
		}
	})

	t.Run("with parent_id returns error when parent not found", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, nil // parent not found
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			ParentID:  "nonexistent",
			UserName:  "bob",
			EventType: model.EventTypeStar,
		})
		if !errors.Is(err, ErrParentNotFound) {
			t.Fatalf("got error %v, want %v", err, ErrParentNotFound)
		}
	})

	t.Run("defaults timestamp to now when not provided", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:  "carol",
			EventType: model.EventTypeK8sChange,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after := time.Now().UTC()

		if got.Timestamp.Before(before) || got.Timestamp.After(after) {
			t.Fatalf("Timestamp %v not in [%v, %v]", got.Timestamp, before, after)
		}
	})

	t.Run("explicit timestamp is preserved", func(t *testing.T) {
		t.Parallel()

		explicit := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:  "bob",
			EventType: model.EventTypeFeatureFlag,
			Timestamp: timePtr(explicit),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Timestamp.Equal(explicit) {
			t.Fatalf("Timestamp = %v, want %v", got.Timestamp, explicit)
		}
	})
}

// ---------------------------------------------------------------------------
// GetByID tests
// ---------------------------------------------------------------------------

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("existing event", func(t *testing.T) {
		t.Parallel()

		want := &model.ChangeEvent{
			ID:       "evt-123",
			UserName: "alice",
		}
		ms := &mockStore{
			getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
				if id != "evt-123" {
					t.Fatalf("GetByID called with %q, want %q", id, "evt-123")
				}
				return want, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.GetByID(context.Background(), "evt-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("not found returns ErrEventNotFound", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, nil
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.GetByID(context.Background(), "missing")
		if !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("got error %v, want %v", err, ErrEventNotFound)
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db read failed")
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.GetByID(context.Background(), "evt-123")
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("params passed through to store", func(t *testing.T) {
		t.Parallel()

		now := time.Now().UTC()
		tags := map[string]string{"team": "infra"}
		input := model.ListParams{
			StartAfter:  timePtr(now.Add(-1 * time.Hour)),
			StartBefore: timePtr(now),
			UserName:    "alice",
			EventType:   model.EventTypeDeployment,
			Tags:        tags,
			Limit:       25,
			Offset:      10,
		}

		var captured model.ListParams
		ms := &mockStore{
			listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
				captured = params
				return &model.ListResult{
					Events:     []model.ChangeEvent{{ID: "e1"}},
					TotalCount: 1,
					Limit:      25,
					Offset:     10,
				}, nil
			},
		}
		svc := NewChangeService(ms)

		result, err := svc.List(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if captured.Limit != 25 {
			t.Errorf("Limit = %d, want 25", captured.Limit)
		}
		if captured.Offset != 10 {
			t.Errorf("Offset = %d, want 10", captured.Offset)
		}
		if captured.UserName != "alice" {
			t.Errorf("UserName = %q, want %q", captured.UserName, "alice")
		}
		if captured.EventType != model.EventTypeDeployment {
			t.Errorf("EventType = %q, want %q", captured.EventType, model.EventTypeDeployment)
		}
		if captured.Tags["team"] != "infra" {
			t.Errorf("Tags[team] = %q, want %q", captured.Tags["team"], "infra")
		}
		if captured.StartAfter == nil || !captured.StartAfter.Equal(*input.StartAfter) {
			t.Errorf("StartAfter = %v, want %v", captured.StartAfter, input.StartAfter)
		}
		if captured.StartBefore == nil || !captured.StartBefore.Equal(*input.StartBefore) {
			t.Errorf("StartBefore = %v, want %v", captured.StartBefore, input.StartBefore)
		}
		if len(result.Events) != 1 || result.Events[0].ID != "e1" {
			t.Errorf("unexpected result events: %+v", result.Events)
		}
	})

	limitClampTests := []struct {
		name      string
		input     int
		wantLimit int
	}{
		{"default limit when zero", 0, model.DefaultLimit},
		{"limit clamped to max 200", 500, 200},
		{"negative limit treated as default", -5, model.DefaultLimit},
	}
	for _, tt := range limitClampTests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var captured model.ListParams
			ms := &mockStore{
				listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
					captured = params
					return &model.ListResult{}, nil
				},
			}
			svc := NewChangeService(ms)

			_, err := svc.List(context.Background(), model.ListParams{Limit: tt.input})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if captured.Limit != tt.wantLimit {
				t.Errorf("Limit = %d, want %d", captured.Limit, tt.wantLimit)
			}
		})
	}

	t.Run("store error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db list failed")
		ms := &mockStore{
			listFn: func(_ context.Context, _ model.ListParams) (*model.ListResult, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.List(context.Background(), model.ListParams{Limit: 10})
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})
}

// ---------------------------------------------------------------------------
// ToggleStar tests
// ---------------------------------------------------------------------------

func TestToggleStar(t *testing.T) {
	t.Parallel()

	t.Run("stars when not starred", func(t *testing.T) {
		t.Parallel()

		parent := &model.ChangeEvent{ID: "evt-1", UserName: "alice"}

		var capturedEvent *model.ChangeEvent
		ms := &mockStore{
			getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
				if id == "evt-1" {
					return parent, nil
				}
				return nil, nil
			},
			getAnnotationsFn: func(_ context.Context, _ string) (*model.EventAnnotations, error) {
				return &model.EventAnnotations{Starred: false, Alerted: false}, nil
			},
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				capturedEvent = event
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.ToggleStar(context.Background(), "evt-1", "bob")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if capturedEvent == nil {
			t.Fatal("store.Create was not called")
		}
		if capturedEvent.EventType != model.EventTypeStar {
			t.Errorf("EventType = %q, want %q", capturedEvent.EventType, model.EventTypeStar)
		}
		if capturedEvent.ParentID != "evt-1" {
			t.Errorf("ParentID = %q, want %q", capturedEvent.ParentID, "evt-1")
		}
		if capturedEvent.UserName != "bob" {
			t.Errorf("UserName = %q, want %q", capturedEvent.UserName, "bob")
		}
		if got.EventType != model.EventTypeStar {
			t.Errorf("returned EventType = %q, want %q", got.EventType, model.EventTypeStar)
		}
	})

	t.Run("unstars when already starred", func(t *testing.T) {
		t.Parallel()

		parent := &model.ChangeEvent{ID: "evt-2", UserName: "alice"}

		var capturedEvent *model.ChangeEvent
		ms := &mockStore{
			getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
				if id == "evt-2" {
					return parent, nil
				}
				return nil, nil
			},
			getAnnotationsFn: func(_ context.Context, _ string) (*model.EventAnnotations, error) {
				return &model.EventAnnotations{Starred: true, Alerted: false}, nil
			},
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				capturedEvent = event
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.ToggleStar(context.Background(), "evt-2", "bob")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if capturedEvent == nil {
			t.Fatal("store.Create was not called")
		}
		if capturedEvent.EventType != model.EventTypeUnstar {
			t.Errorf("EventType = %q, want %q", capturedEvent.EventType, model.EventTypeUnstar)
		}
		if got.EventType != model.EventTypeUnstar {
			t.Errorf("returned EventType = %q, want %q", got.EventType, model.EventTypeUnstar)
		}
	})

	t.Run("not-found parent returns error", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, nil
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.ToggleStar(context.Background(), "nonexistent", "bob")
		if !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("got error %v, want %v", err, ErrEventNotFound)
		}
	})

	t.Run("store GetByID error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db failure")
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.ToggleStar(context.Background(), "evt-1", "bob")
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})
}

// ---------------------------------------------------------------------------
// ExternalID tests
// ---------------------------------------------------------------------------

func TestCreateExternalID(t *testing.T) {
	t.Parallel()

	t.Run("external_id passed to store", func(t *testing.T) {
		t.Parallel()

		var captured *model.ChangeEvent
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				captured = event
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		req := &model.CreateChangeRequest{
			ExternalID:  "gh-actions-run-999",
			UserName:    "alice",
			EventType:   model.EventTypeDeployment,
			Description: "deploy v10",
		}

		got, err := svc.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if captured == nil {
			t.Fatal("store.Create was not called")
		}
		if captured.ExternalID != "gh-actions-run-999" {
			t.Errorf("captured.ExternalID = %q, want %q", captured.ExternalID, "gh-actions-run-999")
		}
		if got.ExternalID != "gh-actions-run-999" {
			t.Errorf("got.ExternalID = %q, want %q", got.ExternalID, "gh-actions-run-999")
		}
	})

	t.Run("duplicate external_id propagates", func(t *testing.T) {
		t.Parallel()

		existing := &model.ChangeEvent{
			ID:         "original-evt",
			ExternalID: "dup-key-1",
			UserName:   "alice",
			EventType:  model.EventTypeDeployment,
		}
		ms := &mockStore{
			createFn: func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
				return existing, store.ErrDuplicate
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			ExternalID: "dup-key-1",
			UserName:   "bob",
			EventType:  model.EventTypeDeployment,
		})
		if !errors.Is(err, store.ErrDuplicate) {
			t.Fatalf("expected store.ErrDuplicate, got %v", err)
		}
		if got == nil {
			t.Fatal("expected existing event to be returned")
		}
		if got.ID != "original-evt" {
			t.Errorf("got.ID = %q, want %q", got.ID, "original-evt")
		}
	})
}
