package service_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sarahmaeve/go-prod-change-registry/internal/fixture"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

// mockStore implements store.ChangeStore using function fields so each test can
// customise behaviour. Any method called without its function field set panics,
// catching unexpected calls early.
type mockStore struct {
	createFn              func(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	toggleStarFn          func(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error)
	toggleAlertFn         func(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error)
	toggleStarIdentityFn  func(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error)
	toggleAlertIdentityFn func(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error)
	getByIDFn             func(ctx context.Context, id string) (*model.ChangeEvent, error)
	getByExternalIDFn     func(ctx context.Context, externalID string) (*model.ChangeEvent, error)
	listFn                func(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	listCurrentFn         func(ctx context.Context, params model.CurrentParams) (*model.ListResult, error)
	getAnnotationsFn      func(ctx context.Context, eventID string) (*model.EventAnnotations, error)
	getAnnotationsBatchFn func(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error)
}

func (m *mockStore) Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
	if m.createFn == nil {
		panic("unexpected call to Create")
	}
	return m.createFn(ctx, event)
}

func (m *mockStore) ToggleStar(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	if m.toggleStarIdentityFn != nil {
		return m.toggleStarIdentityFn(ctx, eventID, user)
	}
	if m.toggleStarFn == nil {
		panic("unexpected call to ToggleStar")
	}
	return m.toggleStarFn(ctx, eventID, user.Name)
}

func (m *mockStore) ToggleAlert(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	if m.toggleAlertIdentityFn != nil {
		return m.toggleAlertIdentityFn(ctx, eventID, user)
	}
	if m.toggleAlertFn == nil {
		panic("unexpected call to ToggleAlert")
	}
	return m.toggleAlertFn(ctx, eventID, user.Name)
}

func (m *mockStore) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	if m.getByIDFn == nil {
		panic("unexpected call to GetByID")
	}
	return m.getByIDFn(ctx, id)
}

func (m *mockStore) GetByExternalID(ctx context.Context, externalID string) (*model.ChangeEvent, error) {
	if m.getByExternalIDFn == nil {
		return nil, nil
	}
	return m.getByExternalIDFn(ctx, externalID)
}

func (m *mockStore) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if m.listFn == nil {
		panic("unexpected call to List")
	}
	return m.listFn(ctx, params)
}

func (m *mockStore) ListCurrent(ctx context.Context, params model.CurrentParams) (*model.ListResult, error) {
	if m.listCurrentFn == nil {
		panic("unexpected call to ListCurrent")
	}
	return m.listCurrentFn(ctx, params)
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
		svc := service.NewChangeService(ms)

		req := &model.CreateChangeRequest{
			UserName:        "alice",
			EventType:       model.EventTypeDeployment,
			Description:     "deploy v42",
			LongDescription: "full rollout of v42",
			Links: []model.EventLink{
				{Label: "Pull request", URL: "https://github.com/example/repo/pull/42"},
				{Label: "Runbook", URL: "https://notion.so/example/runbook"},
			},
			Tags: map[string]string{"env": "prod"},
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
		if len(got.Links) != 2 || got.Links[0].Label != "Pull request" || got.Links[1].URL != "https://notion.so/example/runbook" {
			t.Errorf("Links = %#v, want request links in order", got.Links)
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
		if len(captured.Links) != 2 {
			t.Errorf("captured Links = %#v, want 2 links", captured.Links)
		}
	})

	t.Run("invalid link URL errors before storage", func(t *testing.T) {
		t.Parallel()

		for name, link := range map[string]model.EventLink{
			"script scheme":        {Label: "unsafe", URL: "javascript:alert(1)"},
			"embedded credentials": {Label: "deceptive", URL: "https://trusted.example@evil.example/path"},
			"encoded newline":      {Label: "control", URL: "https://example.com/%0aevil"},
			"backslash":            {Label: "ambiguous", URL: `https://example.com\@evil.example/path`},
			"encoded backslash":    {Label: "ambiguous", URL: `https://example.com/%5c@evil.example/path`},
			"oversized URL":        {Label: "large", URL: "https://example.com/" + strings.Repeat("a", 2049)},
			"control in label":     {Label: "bad\nlabel", URL: "https://example.com/path"},
			"bidi override label":  {Label: "safe\u202Eexample", URL: "https://example.com/path"},
		} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				_, err := service.NewChangeService(&mockStore{}).Create(context.Background(), &model.CreateChangeRequest{
					UserName:  "alice",
					EventType: model.EventTypeDeployment,
					Links:     []model.EventLink{link},
				})
				if !errors.Is(err, service.ErrInvalidLink) {
					t.Fatalf("got error %v, want %v", err, service.ErrInvalidLink)
				}
			})
		}
	})

	t.Run("missing user_name errors", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{} // no createFn -- store should not be called
		svc := service.NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			EventType:   model.EventTypeDeployment,
			Description: "oops",
		})
		if !errors.Is(err, service.ErrUserNameRequired) {
			t.Fatalf("got error %v, want %v", err, service.ErrUserNameRequired)
		}
	})

	t.Run("missing event_type errors", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{} // no createFn -- store should not be called
		svc := service.NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			UserName:    "alice",
			Description: "oops",
		})
		if !errors.Is(err, service.ErrEventTypeRequired) {
			t.Fatalf("got error %v, want %v", err, service.ErrEventTypeRequired)
		}
	})

	t.Run("rejects tags that cannot participate in dashboard filters", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name      string
			eventType string
			tags      map[string]string
			message   string
		}{
			{name: "maintenance without lifecycle", eventType: model.EventTypeMaintenance, tags: map[string]string{"team": "platform"}, message: "maintenance events require"},
			{name: "maintenance without identifier", eventType: model.EventTypeMaintenance, tags: map[string]string{"phase": "start"}, message: "maintenance events require"},
			{name: "maintenance with whitespace identifier", eventType: model.EventTypeMaintenance, tags: map[string]string{"phase": "start", "change_id": "   "}, message: "maintenance events require"},
			{name: "invalid phase", eventType: model.EventTypeDeployment, tags: map[string]string{"phase": "pending", "change_id": "change-1"}, message: "phase must be exactly"},
			{name: "phase without identifier", eventType: model.EventTypeDeployment, tags: map[string]string{"phase": "start"}, message: "phase requires"},
			{name: "identifier without phase", eventType: model.EventTypeDeployment, tags: map[string]string{"change_id": "change-1"}, message: "require phase"},
			{name: "unsupported severity", eventType: model.EventTypeDeployment, tags: map[string]string{"severity": "critical"}, message: "severity must be"},
			{name: "unsupported scope", eventType: model.EventTypeDeployment, tags: map[string]string{"scope": "regional"}, message: "scope must be"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				_, err := service.NewChangeService(&mockStore{}).Create(context.Background(), &model.CreateChangeRequest{
					UserName:  "alice",
					EventType: tc.eventType,
					Tags:      tc.tags,
				})
				if !errors.Is(err, service.ErrInvalidTags) || !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("Create() error = %v, want invalid-tags error containing %q", err, tc.message)
				}
			})
		}
	})

	t.Run("accepts maintenance lifecycle and normalizes filter tags", func(t *testing.T) {
		t.Parallel()

		var captured *model.ChangeEvent
		ms := &mockStore{createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			captured = event
			return event, nil
		}}
		requestTags := map[string]string{
			"phase": " end ", "deploy_id": " waf-pop2 ", "team": " platform ", "severity": " SEV1 ", "scope": " SITE ",
		}
		created, err := service.NewChangeService(ms).Create(context.Background(), &model.CreateChangeRequest{
			UserName: "alice", EventType: " Maintenance ", Tags: requestTags,
		})
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if captured == nil || captured.Tags["phase"] != "end" || captured.Tags["deploy_id"] != "waf-pop2" ||
			captured.Tags["team"] != "platform" || captured.Tags["severity"] != "sev1" || captured.Tags["scope"] != "site" {
			t.Fatalf("stored tags = %v, want normalized well-known tags", captured.Tags)
		}
		if created.EventType != model.EventTypeMaintenance {
			t.Fatalf("stored event type = %q, want maintenance", created.EventType)
		}
		if requestTags["deploy_id"] != " waf-pop2 " || requestTags["severity"] != " SEV1 " || requestTags["scope"] != " SITE " {
			t.Fatalf("Create() mutated request tags: %v", requestTags)
		}
	})

	t.Run("invalid legacy retry still returns existing external ID", func(t *testing.T) {
		t.Parallel()

		existing := &model.ChangeEvent{ID: "existing", ExternalID: "legacy-maintenance"}
		ms := &mockStore{getByExternalIDFn: func(_ context.Context, externalID string) (*model.ChangeEvent, error) {
			if externalID != existing.ExternalID {
				t.Errorf("GetByExternalID(%q), want %q", externalID, existing.ExternalID)
			}
			return existing, nil
		}}
		got, err := service.NewChangeService(ms).Create(context.Background(), &model.CreateChangeRequest{
			ExternalID: existing.ExternalID,
			EventType:  model.EventTypeMaintenance,
			Tags:       map[string]string{"team": "platform"},
		})
		if got != existing || !errors.Is(err, store.ErrDuplicate) {
			t.Fatalf("Create() = (%p, %v), want existing event and duplicate error", got, err)
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
		svc := service.NewChangeService(ms)

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
		svc := service.NewChangeService(ms)

		_, err := svc.Create(context.Background(), &model.CreateChangeRequest{
			ParentID:  "nonexistent",
			UserName:  "bob",
			EventType: model.EventTypeStar,
		})
		if !errors.Is(err, service.ErrParentNotFound) {
			t.Fatalf("got error %v, want %v", err, service.ErrParentNotFound)
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
		svc := service.NewChangeService(ms)

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

	t.Run("explicit timestamp is normalized to UTC", func(t *testing.T) {
		t.Parallel()

		explicit := time.Date(2025, 6, 15, 13, 0, 0, 0, time.FixedZone("UTC+1", 60*60))
		want := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
		ms := &mockStore{
			createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				cp := *event
				return &cp, nil
			},
		}
		svc := service.NewChangeService(ms)

		got, err := svc.Create(t.Context(), &model.CreateChangeRequest{
			UserName:  "bob",
			EventType: model.EventTypeFeatureFlag,
			Timestamp: new(explicit),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Timestamp.Equal(want) || got.Timestamp.Location() != time.UTC {
			t.Fatalf("Timestamp = %v (%v), want %v (UTC)", got.Timestamp, got.Timestamp.Location(), want)
		}
	})
}

func TestDemoFixtureUsesAcceptedNewEventShapes(t *testing.T) {
	t.Parallel()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve service test path")
	}
	fixturePath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "testdata", "functional", "phosphor-demo.json")
	file, err := os.Open(fixturePath) //nolint:gosec // G304: path is rooted at this test source file
	if err != nil {
		t.Fatalf("open demo fixture: %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })

	events, err := fixture.Load(file)
	if err != nil {
		t.Fatalf("load demo fixture: %v", err)
	}
	eventsByID := make(map[string]*model.ChangeEvent)
	ms := &mockStore{}
	ms.createFn = func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
		eventsByID[event.ID] = event
		return event, nil
	}
	ms.getByIDFn = func(_ context.Context, id string) (*model.ChangeEvent, error) {
		return eventsByID[id], nil
	}

	if _, err := fixture.Apply(t.Context(), service.NewChangeService(ms), events); err != nil {
		t.Fatalf("apply demo fixture through ChangeService: %v", err)
	}
}

func TestAddLinks(t *testing.T) {
	t.Parallel()

	parent := &model.ChangeEvent{ID: "parent-1", UserName: "alice", EventType: model.EventTypeDeployment}
	var created *model.ChangeEvent
	ms := &mockStore{
		getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id != parent.ID {
				t.Errorf("GetByID(%q), want %q", id, parent.ID)
			}
			return parent, nil
		},
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			created = event
			return event, nil
		},
	}
	svc := service.NewChangeService(ms)
	links := []model.EventLink{
		{Label: "Incident", URL: "https://example.pagerduty.com/incidents/P1"},
		{Label: "Plan", URL: "https://notion.so/example/plan"},
	}
	got, err := svc.AddLinks(t.Context(), parent.ID, "bob", links)
	if err != nil {
		t.Fatalf("AddLinks() error = %v", err)
	}
	if created == nil || got != created {
		t.Fatal("AddLinks() did not return the created annotation")
	}
	if created.ParentID != parent.ID || created.EventType != model.EventTypeLink || created.UserName != "bob" {
		t.Errorf("created annotation = %+v", created)
	}
	if len(created.Links) != 2 || created.Links[0].Label != "Incident" || created.Links[1].Label != "Plan" {
		t.Errorf("created links = %#v", created.Links)
	}
}

func TestAddLinksRequiresAtLeastOneLink(t *testing.T) {
	t.Parallel()

	svc := service.NewChangeService(&mockStore{})
	_, err := svc.AddLinks(t.Context(), "parent-1", "alice", nil)
	if !errors.Is(err, service.ErrLinksRequired) {
		t.Fatalf("AddLinks() error = %v, want %v", err, service.ErrLinksRequired)
	}
}

func TestGetActivityReturnsOldestFirst(t *testing.T) {
	t.Parallel()

	newer := model.ChangeEvent{ID: "newer", ParentID: "parent-1", Timestamp: time.Date(2026, 8, 24, 12, 2, 0, 0, time.UTC)}
	older := model.ChangeEvent{ID: "older", ParentID: "parent-1", Timestamp: time.Date(2026, 8, 24, 12, 1, 0, 0, time.UTC)}
	ms := &mockStore{
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "parent-1"}, nil
		},
		listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			if params.ParentID != "parent-1" || params.Limit != model.MaxLimit {
				t.Errorf("List params = %+v", params)
			}
			return &model.ListResult{Events: []model.ChangeEvent{newer, older}, TotalCount: 2}, nil
		},
	}
	activity, err := service.NewChangeService(ms).GetActivity(t.Context(), "parent-1")
	if err != nil {
		t.Fatalf("GetActivity() error = %v", err)
	}
	if len(activity) != 2 || activity[0].ID != "older" || activity[1].ID != "newer" {
		t.Errorf("activity = %#v, want oldest first", activity)
	}
}

func TestGetActivityIncludesCorrelatedClosure(t *testing.T) {
	t.Parallel()

	start := &model.ChangeEvent{ID: "start", EventType: "deployment", Tags: map[string]string{"phase": "start", "change_id": "change-1"}}
	closure := model.ChangeEvent{ID: "end", EventType: "deployment", Description: "rollout complete", Tags: map[string]string{"phase": "end", "change_id": "change-1"}}
	ms := &mockStore{
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) { return start, nil },
		listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			if params.ParentID != "" {
				return &model.ListResult{}, nil
			}
			if !params.TopLevel || params.EventType != "deployment" || params.Tags["phase"] != "end" || params.Tags["change_id"] != "change-1" {
				t.Errorf("closure List params = %+v", params)
			}
			return &model.ListResult{Events: []model.ChangeEvent{closure}}, nil
		},
	}
	activity, err := service.NewChangeService(ms).GetActivity(t.Context(), start.ID)
	if err != nil {
		t.Fatalf("GetActivity() error = %v", err)
	}
	if len(activity) != 1 || activity[0].ID != closure.ID {
		t.Errorf("activity = %#v, want closure", activity)
	}
}

func TestGetActivityPaginatesPastMaxListSize(t *testing.T) {
	t.Parallel()

	firstPage := make([]model.ChangeEvent, model.MaxLimit)
	for i := range firstPage {
		firstPage[i] = model.ChangeEvent{ID: fmt.Sprintf("event-%03d", i), Timestamp: time.Unix(int64(i), 0)}
	}
	last := model.ChangeEvent{ID: "event-200", Timestamp: time.Unix(model.MaxLimit, 0)}
	ms := &mockStore{
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "parent"}, nil
		},
		listFn: func(_ context.Context, params model.ListParams) (*model.ListResult, error) {
			switch params.Offset {
			case 0:
				return &model.ListResult{Events: firstPage, TotalCount: model.MaxLimit + 1}, nil
			case model.MaxLimit:
				return &model.ListResult{Events: []model.ChangeEvent{last}, TotalCount: model.MaxLimit + 1}, nil
			default:
				t.Fatalf("unexpected activity offset %d", params.Offset)
				return nil, nil
			}
		},
	}
	activity, err := service.NewChangeService(ms).GetActivity(t.Context(), "parent")
	if err != nil {
		t.Fatalf("GetActivity() error = %v", err)
	}
	if len(activity) != model.MaxLimit+1 {
		t.Fatalf("activity length = %d, want %d", len(activity), model.MaxLimit+1)
	}
}

func TestCloseOperation(t *testing.T) {
	t.Parallel()

	start := &model.ChangeEvent{
		ID:          "start-1",
		UserName:    "alice",
		EventType:   model.EventTypeDeployment,
		Description: "payments rollout",
		Tags: map[string]string{
			"phase": "start", "change_id": "change-1", "team": "payments", "severity": "sev2",
		},
	}
	var closed *model.ChangeEvent
	ms := &mockStore{
		getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
			if id != start.ID {
				t.Errorf("GetByID(%q), want %q", id, start.ID)
			}
			return start, nil
		},
		listCurrentFn: func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			if params.CorrelationKey != "change_id" || params.CorrelationValue != "change-1" || params.EventType != model.EventTypeDeployment {
				t.Errorf("ListCurrent params = %+v", params)
			}
			return &model.ListResult{Events: []model.ChangeEvent{*start}, TotalCount: 1}, nil
		},
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			closed = event
			return event, nil
		},
	}
	got, err := service.NewChangeService(ms).CloseOperation(t.Context(), start.ID, "bob", "rollout completed safely")
	if err != nil {
		t.Fatalf("CloseOperation() error = %v", err)
	}
	if got != closed || closed == nil {
		t.Fatal("CloseOperation() did not return the appended event")
	}
	if closed.ParentID != "" || closed.EventType != start.EventType || closed.UserName != "bob" || closed.Description != "rollout completed safely" {
		t.Errorf("closure = %+v", closed)
	}
	wantExternalID := fmt.Sprintf("pcr:close:%x", sha256.Sum256([]byte("deployment\x00change_id\x00change-1")))
	if closed.ExternalID != wantExternalID || closed.Tags["phase"] != "end" || closed.Tags["change_id"] != "change-1" || closed.Tags["team"] != "payments" {
		t.Errorf("closure identity/tags = external %q tags %#v", closed.ExternalID, closed.Tags)
	}
}

func TestCloseOperationPreservesLegacyTags(t *testing.T) {
	t.Parallel()

	start := &model.ChangeEvent{
		ID:          "legacy-start",
		UserName:    "alice",
		EventType:   model.EventTypeDeployment,
		Description: "legacy rollout",
		Tags: map[string]string{
			"phase": "start", "change_id": " legacy-id ", "severity": "critical", "scope": "regional",
		},
	}
	var closed *model.ChangeEvent
	ms := &mockStore{
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return start, nil
		},
		listCurrentFn: func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			if params.CorrelationValue != " legacy-id " {
				t.Errorf("ListCurrent() correlation value = %q, want %q", params.CorrelationValue, " legacy-id ")
			}
			return &model.ListResult{Events: []model.ChangeEvent{*start}, TotalCount: 1}, nil
		},
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
			closed = event
			return event, nil
		},
	}

	if _, err := service.NewChangeService(ms).CloseOperation(t.Context(), start.ID, "bob", ""); err != nil {
		t.Fatalf("CloseOperation() error = %v", err)
	}
	if closed == nil {
		t.Fatal("CloseOperation() did not append an event")
	}
	if closed.Tags["change_id"] != " legacy-id " || closed.Tags["severity"] != "critical" || closed.Tags["scope"] != "regional" {
		t.Errorf("closure tags = %#v, want legacy values preserved", closed.Tags)
	}
}

func TestCloseOperationRejectsInvalidOrClosedEvents(t *testing.T) {
	t.Parallel()

	t.Run("event is not a correlated start", func(t *testing.T) {
		t.Parallel()
		ms := &mockStore{getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			return &model.ChangeEvent{ID: "plain", EventType: "maintenance", Tags: map[string]string{"phase": "start"}}, nil
		}}
		_, err := service.NewChangeService(ms).CloseOperation(t.Context(), "plain", "bob", "")
		if !errors.Is(err, service.ErrOperationNotClosable) {
			t.Fatalf("error = %v, want %v", err, service.ErrOperationNotClosable)
		}
	})

	t.Run("operation already ended", func(t *testing.T) {
		t.Parallel()
		ms := &mockStore{
			getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
				return &model.ChangeEvent{ID: "closed", EventType: "maintenance", Tags: map[string]string{"phase": "start", "deploy_id": "deploy-1"}}, nil
			},
			listCurrentFn: func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
				return &model.ListResult{}, nil
			},
		}
		_, err := service.NewChangeService(ms).CloseOperation(t.Context(), "closed", "bob", "")
		if !errors.Is(err, service.ErrOperationClosed) {
			t.Fatalf("error = %v, want %v", err, service.ErrOperationClosed)
		}
	})
}

func TestOperationState(t *testing.T) {
	t.Parallel()

	start := &model.ChangeEvent{ID: "start", EventType: "maintenance", Tags: map[string]string{"phase": "start", "change_id": "change-1"}}
	ms := &mockStore{listCurrentFn: func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
		return &model.ListResult{TotalCount: 1}, nil
	}}
	state, err := service.NewChangeService(ms).OperationState(t.Context(), start)
	if err != nil || state != model.OperationStateOpen {
		t.Fatalf("OperationState() = %q, %v; want open", state, err)
	}
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
		svc := service.NewChangeService(ms)

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
		svc := service.NewChangeService(ms)

		_, err := svc.GetByID(context.Background(), "missing")
		if !errors.Is(err, service.ErrEventNotFound) {
			t.Fatalf("got error %v, want %v", err, service.ErrEventNotFound)
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
		svc := service.NewChangeService(ms)

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
			StartAfter:  new(now.Add(-1 * time.Hour)),
			StartBefore: new(now),
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
		svc := service.NewChangeService(ms)

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
			svc := service.NewChangeService(ms)

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
		svc := service.NewChangeService(ms)

		_, err := svc.List(context.Background(), model.ListParams{Limit: 10})
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})
}

func TestListCurrent(t *testing.T) {
	t.Parallel()

	want := &model.ListResult{Events: []model.ChangeEvent{{ID: "active"}}, TotalCount: 1}
	var captured model.CurrentParams
	storeErr := errors.New("current query failed")

	t.Run("normalizes limit and delegates", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			listCurrentFn: func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
				captured = params
				return want, nil
			},
		}
		svc := service.NewChangeService(ms)
		result, err := svc.ListCurrent(t.Context(), model.CurrentParams{
			ForTeam:    "payments",
			Scopes:     []string{"site"},
			Severities: []string{"sev0", "sev1"},
			EventType:  "deployment",
			Offset:     10,
		})
		if err != nil {
			t.Fatalf("ListCurrent() error = %v", err)
		}
		if result != want {
			t.Errorf("ListCurrent() result = %p, want %p", result, want)
		}
		if captured.Limit != model.DefaultLimit {
			t.Errorf("Limit = %d, want %d", captured.Limit, model.DefaultLimit)
		}
		if captured.ForTeam != "payments" || captured.EventType != "deployment" || captured.Offset != 10 {
			t.Errorf("captured params = %+v", captured)
		}
		if fmt.Sprint(captured.Scopes) != "[site]" || fmt.Sprint(captured.Severities) != "[sev0 sev1]" {
			t.Errorf("captured slice params = %+v", captured)
		}
	})

	t.Run("propagates store error", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			listCurrentFn: func(_ context.Context, _ model.CurrentParams) (*model.ListResult, error) {
				return nil, storeErr
			},
		}
		svc := service.NewChangeService(ms)
		_, err := svc.ListCurrent(t.Context(), model.CurrentParams{Limit: model.MaxLimit + 1})
		if !errors.Is(err, storeErr) {
			t.Errorf("ListCurrent() error = %v, want %v", err, storeErr)
		}
	})
}

// ---------------------------------------------------------------------------
// ToggleStar tests
// ---------------------------------------------------------------------------

func TestToggleStar(t *testing.T) {
	t.Parallel()

	t.Run("delegates atomic toggle to store", func(t *testing.T) {
		t.Parallel()

		want := &model.ChangeEvent{ID: "star-1", ParentID: "evt-1", UserName: "bob", EventType: model.EventTypeStar}
		ms := &mockStore{
			toggleStarFn: func(_ context.Context, eventID, userName string) (*model.ChangeEvent, error) {
				if eventID != "evt-1" {
					t.Errorf("eventID = %q, want evt-1", eventID)
				}
				if userName != "bob" {
					t.Errorf("userName = %q, want bob", userName)
				}
				return want, nil
			},
		}
		svc := service.NewChangeService(ms)

		got, err := svc.ToggleStar(context.Background(), "evt-1", "bob")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != want {
			t.Errorf("ToggleStar() = %p, want %p", got, want)
		}
	})

	t.Run("not-found parent maps to service error", func(t *testing.T) {
		t.Parallel()

		ms := &mockStore{
			toggleStarFn: func(_ context.Context, _, _ string) (*model.ChangeEvent, error) {
				return nil, store.ErrNotFound
			},
		}
		svc := service.NewChangeService(ms)

		_, err := svc.ToggleStar(context.Background(), "nonexistent", "bob")
		if !errors.Is(err, service.ErrEventNotFound) {
			t.Fatalf("got error %v, want %v", err, service.ErrEventNotFound)
		}
	})

	t.Run("store error propagates", func(t *testing.T) {
		t.Parallel()

		storeErr := errors.New("db failure")
		ms := &mockStore{
			toggleStarFn: func(_ context.Context, _, _ string) (*model.ChangeEvent, error) {
				return nil, storeErr
			},
		}
		svc := service.NewChangeService(ms)

		_, err := svc.ToggleStar(context.Background(), "evt-1", "bob")
		if !errors.Is(err, storeErr) {
			t.Fatalf("got error %v, want %v", err, storeErr)
		}
	})
}

func TestToggleAlert(t *testing.T) {
	t.Parallel()

	want := &model.ChangeEvent{ID: "alert-1", ParentID: "evt-1", EventType: model.EventTypeAlert}
	ms := &mockStore{toggleAlertFn: func(_ context.Context, eventID, userName string) (*model.ChangeEvent, error) {
		if eventID != "evt-1" || userName != "bob" {
			t.Errorf("ToggleAlert(%q, %q)", eventID, userName)
		}
		return want, nil
	}}
	got, err := service.NewChangeService(ms).ToggleAlert(t.Context(), "evt-1", "bob")
	if err != nil {
		t.Fatalf("ToggleAlert() error = %v", err)
	}
	if got != want {
		t.Errorf("ToggleAlert() = %p, want %p", got, want)
	}

	ms.toggleAlertFn = func(_ context.Context, _, _ string) (*model.ChangeEvent, error) {
		return nil, store.ErrNotFound
	}
	_, err = service.NewChangeService(ms).ToggleAlert(t.Context(), "missing", "bob")
	if !errors.Is(err, service.ErrEventNotFound) {
		t.Fatalf("ToggleAlert(missing) error = %v, want %v", err, service.ErrEventNotFound)
	}
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
		svc := service.NewChangeService(ms)

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
		svc := service.NewChangeService(ms)

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
