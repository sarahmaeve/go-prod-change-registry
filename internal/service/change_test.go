package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// mockStore implements store.ChangeStore using function fields so each test can
// customise behaviour. Any method called without its function field set panics,
// catching unexpected calls early.
type mockStore struct {
	createFn  func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	getByIDFn func(ctx context.Context, id string) (*model.ChangeEvent, error)
	updateFn  func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	deleteFn  func(ctx context.Context, id string) error
	listFn    func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
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

func (m *mockStore) Update(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.updateFn == nil {
		panic("unexpected call to Update")
	}
	return m.updateFn(ctx, event)
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	if m.deleteFn == nil {
		panic("unexpected call to Delete")
	}
	return m.deleteFn(ctx, id)
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn == nil {
		panic("unexpected call to List")
	}
	return m.listFn(ctx, params)
}

func (m *mockStore) Close() error { return nil }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string       { return &s }
func timePtr(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("successful create with all fields defaults TimestampStart to now", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()

		var captured *model.ChangeEvent
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				captured = event
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

		// CreatedAt and UpdatedAt should be equal and within [before, after].
		if got.CreatedAt != got.UpdatedAt {
			t.Fatalf("CreatedAt (%v) != UpdatedAt (%v)", got.CreatedAt, got.UpdatedAt)
		}
		if got.CreatedAt.Before(before) || got.CreatedAt.After(after) {
			t.Fatalf("CreatedAt %v not in [%v, %v]", got.CreatedAt, before, after)
		}

		// TimestampStart should default to ~now.
		if got.TimestampStart.Before(before) || got.TimestampStart.After(after) {
			t.Fatalf("TimestampStart %v not in [%v, %v]", got.TimestampStart, before, after)
		}

		// Verify remaining fields.
		if got.UserName != "alice" {
			t.Fatalf("UserName = %q, want %q", got.UserName, "alice")
		}
		if got.EventType != model.EventTypeDeployment {
			t.Fatalf("EventType = %q, want %q", got.EventType, model.EventTypeDeployment)
		}
		if got.Description != "deploy v42" {
			t.Fatalf("Description = %q, want %q", got.Description, "deploy v42")
		}
		if got.LongDescription != "full rollout of v42" {
			t.Fatalf("LongDescription = %q, want %q", got.LongDescription, "full rollout of v42")
		}
		if got.Tags["env"] != "prod" {
			t.Fatalf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
		}

		// Verify the mock received the correct values.
		if captured == nil {
			t.Fatal("store.Create was not called")
		}
		if captured.ID == "" {
			t.Fatal("store received event with empty ID")
		}
		if captured.UserName != "alice" {
			t.Fatalf("store received UserName %q, want %q", captured.UserName, "alice")
		}
		if captured.EventType != model.EventTypeDeployment {
			t.Fatalf("store received EventType %q, want %q", captured.EventType, model.EventTypeDeployment)
		}
		if captured.Description != "deploy v42" {
			t.Fatalf("store received Description %q, want %q", captured.Description, "deploy v42")
		}
	})

	t.Run("explicit TimestampStart is preserved", func(t *testing.T) {
		t.Parallel()

		explicit := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		var captured *model.ChangeEvent
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				captured = event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		req := &model.CreateChangeRequest{
			UserName:       "bob",
			EventType:      model.EventTypeFeatureFlag,
			Description:    "toggle dark-mode",
			TimestampStart: timePtr(explicit),
		}

		got, err := svc.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.TimestampStart.Equal(explicit) {
			t.Fatalf("TimestampStart = %v, want %v", got.TimestampStart, explicit)
		}
		// Verify the mock received the event with expected values.
		if captured == nil {
			t.Fatal("store.Create was not called")
		}
		if captured.UserName != "bob" {
			t.Fatalf("store received UserName %q, want %q", captured.UserName, "bob")
		}
	})

	t.Run("empty UserName returns ErrUserNameRequired", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{} // no createFn — store should not be called
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			EventType:   model.EventTypeDeployment,
			Description: "oops",
		})
		if !errors.Is(err, ErrUserNameRequired) {
			t.Fatalf("got error %v, want %v", err, ErrUserNameRequired)
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("create failed")
		ms := &mockStore{
			createFn: func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:    "alice",
			EventType:   model.EventTypeDeployment,
			Description: "deploy v1",
		})
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})

	t.Run("event passed to store has generated ID and timestamps", func(t *testing.T) {
		t.Parallel()

		before := time.Now().UTC()

		var captured *model.ChangeEvent
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				captured = event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:    "carol",
			EventType:   model.EventTypeK8sChange,
			Description: "scale replicas",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		after := time.Now().UTC()

		if captured == nil {
			t.Fatal("store.Create was not called")
		}
		if captured.ID == "" {
			t.Fatal("store received event with empty ID")
		}
		if captured.CreatedAt.Before(before) || captured.CreatedAt.After(after) {
			t.Fatalf("store event CreatedAt %v not in [%v, %v]", captured.CreatedAt, before, after)
		}
		if captured.UpdatedAt.Before(before) || captured.UpdatedAt.After(after) {
			t.Fatalf("store event UpdatedAt %v not in [%v, %v]", captured.UpdatedAt, before, after)
		}
	})
}

// ---------------------------------------------------------------------------
// GetByID tests
// ---------------------------------------------------------------------------

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("successful get returns event from store", func(t *testing.T) {
		t.Parallel()

		want := &model.ChangeEvent{
			ID:       "evt-123",
			UserName: "alice",
		}
		ms := &mockStore{
			getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
				if id != "evt-123" {
					t.Fatalf("GetByID called with id %q, want %q", id, "evt-123")
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

	t.Run("store returns nil yields ErrEventNotFound", func(t *testing.T) {
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
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestUpdate(t *testing.T) {
	t.Parallel()

	t.Run("update with all fields changed", func(t *testing.T) {
		t.Parallel()

		existing := &model.ChangeEvent{
			ID:              "evt-1",
			UserName:        "alice",
			EventType:       model.EventTypeDeployment,
			Description:     "old desc",
			LongDescription: "old long",
			TimestampStart:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			Tags:            map[string]string{"env": "staging"},
			CreatedAt:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		newEnd := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
		newStart := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
		newTags := map[string]string{"env": "prod", "team": "platform"}

		var captured *model.ChangeEvent
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				// Return a copy so we can verify mutations.
				cp := *existing
				cp.Tags = map[string]string{"env": "staging"}
				return &cp, nil
			},
			updateFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				captured = event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		before := time.Now().UTC()
		got, err := svc.Update(context.Background(), "evt-1", &model.UpdateChangeRequest{
			UserName:        strPtr("bob"),
			EventType:       strPtr(model.EventTypeFeatureFlag),
			Description:     strPtr("new desc"),
			LongDescription: strPtr("new long"),
			TimestampStart:  timePtr(newStart),
			TimestampEnd:    timePtr(newEnd),
			Tags:            &newTags,
		})
		after := time.Now().UTC()

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.UserName != "bob" {
			t.Fatalf("UserName = %q, want %q", got.UserName, "bob")
		}
		if got.EventType != model.EventTypeFeatureFlag {
			t.Fatalf("EventType = %q, want %q", got.EventType, model.EventTypeFeatureFlag)
		}
		if got.Description != "new desc" {
			t.Fatalf("Description = %q, want %q", got.Description, "new desc")
		}
		if got.LongDescription != "new long" {
			t.Fatalf("LongDescription = %q, want %q", got.LongDescription, "new long")
		}
		if !got.TimestampStart.Equal(newStart) {
			t.Fatalf("TimestampStart = %v, want %v", got.TimestampStart, newStart)
		}
		if got.TimestampEnd == nil || !got.TimestampEnd.Equal(newEnd) {
			t.Fatalf("TimestampEnd = %v, want %v", got.TimestampEnd, newEnd)
		}
		if got.Tags["env"] != "prod" || got.Tags["team"] != "platform" {
			t.Fatalf("Tags = %v, want %v", got.Tags, newTags)
		}
		// UpdatedAt must be bumped.
		if captured.UpdatedAt.Before(before) || captured.UpdatedAt.After(after) {
			t.Fatalf("UpdatedAt %v not in [%v, %v]", captured.UpdatedAt, before, after)
		}
	})

	t.Run("partial update preserves unchanged fields", func(t *testing.T) {
		t.Parallel()

		existing := &model.ChangeEvent{
			ID:              "evt-2",
			UserName:        "alice",
			EventType:       model.EventTypeDeployment,
			Description:     "keep me",
			LongDescription: "keep me long",
			TimestampStart:  time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
			Tags:            map[string]string{"env": "prod"},
			CreatedAt:       time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:       time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC),
		}

		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				cp := *existing
				cp.Tags = map[string]string{"env": "prod"}
				return &cp, nil
			},
			updateFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		got, err := svc.Update(context.Background(), "evt-2", &model.UpdateChangeRequest{
			Description: strPtr("updated desc"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Changed field.
		if got.Description != "updated desc" {
			t.Fatalf("Description = %q, want %q", got.Description, "updated desc")
		}
		// Unchanged fields.
		if got.UserName != "alice" {
			t.Fatalf("UserName = %q, want %q", got.UserName, "alice")
		}
		if got.EventType != model.EventTypeDeployment {
			t.Fatalf("EventType = %q, want %q", got.EventType, model.EventTypeDeployment)
		}
		if got.LongDescription != "keep me long" {
			t.Fatalf("LongDescription = %q, want %q", got.LongDescription, "keep me long")
		}
		if got.Tags["env"] != "prod" {
			t.Fatalf("Tags[env] = %q, want %q", got.Tags["env"], "prod")
		}
	})

	t.Run("update non-existent event returns ErrEventNotFound", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, nil
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Update(context.Background(), "no-such-id", &model.UpdateChangeRequest{
			Description: strPtr("won't work"),
		})
		if !errors.Is(err, ErrEventNotFound) {
			t.Fatalf("got error %v, want %v", err, ErrEventNotFound)
		}
	})

	t.Run("store GetByID error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db lookup failed")
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Update(context.Background(), "evt-1", &model.UpdateChangeRequest{
			Description: strPtr("new desc"),
		})
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})

	t.Run("store Update error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db write failed")
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return &model.ChangeEvent{
					ID:       "evt-1",
					UserName: "alice",
				}, nil
			},
			updateFn: func(_ context.Context, _ *model.ChangeEvent) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := NewChangeService(ms)

		_, err := svc.Update(context.Background(), "evt-1", &model.UpdateChangeRequest{
			Description: strPtr("new desc"),
		})
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})

	t.Run("UpdatedAt is bumped", func(t *testing.T) {
		t.Parallel()

		oldUpdated := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return &model.ChangeEvent{
					ID:        "evt-3",
					UserName:  "alice",
					UpdatedAt: oldUpdated,
				}, nil
			},
			updateFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				return &cp, nil
			},
		}
		svc := NewChangeService(ms)

		before := time.Now().UTC()
		got, err := svc.Update(context.Background(), "evt-3", &model.UpdateChangeRequest{
			UserName: strPtr("bob"),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.UpdatedAt.After(oldUpdated) {
			t.Fatalf("UpdatedAt %v was not bumped past %v", got.UpdatedAt, oldUpdated)
		}
		if got.UpdatedAt.Before(before) {
			t.Fatalf("UpdatedAt %v is before test start %v", got.UpdatedAt, before)
		}
	})
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestDelete(t *testing.T) {
	t.Parallel()

	t.Run("successful delete passes through to store", func(t *testing.T) {
		t.Parallel()

		var calledWith string
		ms := &mockStore{
			deleteFn: func(_ context.Context, id string) error {
				calledWith = id
				return nil
			},
		}
		svc := NewChangeService(ms)

		err := svc.Delete(context.Background(), "evt-del")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calledWith != "evt-del" {
			t.Fatalf("store.Delete called with %q, want %q", calledWith, "evt-del")
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db connection lost")
		ms := &mockStore{
			deleteFn: func(_ context.Context, _ string) error {
				return storeErr
			},
		}
		svc := NewChangeService(ms)

		err := svc.Delete(context.Background(), "evt-x")
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
		tt := tt
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
				t.Fatalf("Limit = %d, want %d", captured.Limit, tt.wantLimit)
			}
		})
	}

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
			t.Fatalf("Limit = %d, want 25", captured.Limit)
		}
		if captured.Offset != 10 {
			t.Fatalf("Offset = %d, want 10", captured.Offset)
		}
		if captured.UserName != "alice" {
			t.Fatalf("UserName = %q, want %q", captured.UserName, "alice")
		}
		if captured.EventType != model.EventTypeDeployment {
			t.Fatalf("EventType = %q, want %q", captured.EventType, model.EventTypeDeployment)
		}
		if captured.Tags["team"] != "infra" {
			t.Fatalf("Tags[team] = %q, want %q", captured.Tags["team"], "infra")
		}
		if captured.StartAfter == nil || !captured.StartAfter.Equal(*input.StartAfter) {
			t.Fatalf("StartAfter = %v, want %v", captured.StartAfter, input.StartAfter)
		}
		if captured.StartBefore == nil || !captured.StartBefore.Equal(*input.StartBefore) {
			t.Fatalf("StartBefore = %v, want %v", captured.StartBefore, input.StartBefore)
		}
		if len(result.Events) != 1 || result.Events[0].ID != "e1" {
			t.Fatalf("unexpected result events: %+v", result.Events)
		}
	})
}
