package service_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/service"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

func TestCreateValidationErrorPrecedence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		userName  string
		eventType string
		tags      map[string]string
		want      error
		message   string
	}{
		{name: "user before event type", want: service.ErrUserNameRequired},
		{name: "event type before tags", userName: "alice", tags: map[string]string{"phase": "pending"}, want: service.ErrEventTypeRequired},
		{name: "maintenance before phase", userName: "alice", eventType: " Maintenance ", tags: map[string]string{"phase": "pending", "severity": "critical"}, want: service.ErrInvalidTags, message: "maintenance events require phase=start or phase=end and a non-empty change_id or deploy_id"},
		{name: "phase before identifier", userName: "alice", eventType: model.EventTypeDeployment, tags: map[string]string{"phase": "pending", "severity": "critical"}, want: service.ErrInvalidTags, message: "phase must be exactly start or end"},
		{name: "identifier before classification", userName: "alice", eventType: model.EventTypeDeployment, tags: map[string]string{"phase": "start", "severity": "critical"}, want: service.ErrInvalidTags, message: "phase requires a non-empty change_id or deploy_id"},
		{name: "missing phase before classification", userName: "alice", eventType: model.EventTypeDeployment, tags: map[string]string{"change_id": "change-1", "severity": "critical"}, want: service.ErrInvalidTags, message: "change_id and deploy_id require phase=start or phase=end"},
		{name: "severity before scope", userName: "alice", eventType: model.EventTypeDeployment, tags: map[string]string{"severity": "critical", "scope": "regional"}, want: service.ErrInvalidTags, message: "severity must be sev0, sev1, sev2, or sev3"},
		{name: "scope before links", userName: "alice", eventType: model.EventTypeDeployment, tags: map[string]string{"scope": "regional"}, want: service.ErrInvalidTags, message: "scope must be service, system, or site"},
		{name: "links before parent", userName: "alice", eventType: model.EventTypeDeployment, want: service.ErrInvalidLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := service.NewChangeService(&mockStore{}).Create(t.Context(), &model.CreateChangeRequest{
				ExternalID: "new-event",
				ParentID:   "missing-parent",
				UserName:   tc.userName,
				EventType:  tc.eventType,
				Tags:       tc.tags,
				Links:      []model.EventLink{{URL: "javascript:alert(1)"}},
			})
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("Create() = (%v, %v), want nil and %v", got, err, tc.want)
			}
			if tc.message != "" && err.Error() != tc.message {
				t.Errorf("Create() error = %q, want %q", err.Error(), tc.message)
			}
		})
	}
}

func TestCreateExternalIDLookupPrecedesValidationAndParent(t *testing.T) {
	t.Parallel()

	existing := &model.ChangeEvent{ID: "existing", ExternalID: "retry"}
	lookupErr := errors.New("external ID lookup failed")
	for _, tc := range []struct {
		name     string
		existing *model.ChangeEvent
		lookup   error
		want     error
	}{
		{name: "duplicate", existing: existing, want: store.ErrDuplicate},
		{name: "lookup failure", lookup: lookupErr, want: lookupErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			ms := &mockStore{getByExternalIDFn: func(_ context.Context, externalID string) (*model.ChangeEvent, error) {
				lookups++
				if externalID != "retry" {
					t.Errorf("GetByExternalID(%q), want retry", externalID)
				}
				return tc.existing, tc.lookup
			}}
			got, err := service.NewChangeService(ms).Create(t.Context(), &model.CreateChangeRequest{
				ExternalID: "retry",
				ParentID:   "missing-parent",
				Tags:       map[string]string{"severity": "critical"},
				Links:      []model.EventLink{{URL: "javascript:alert(1)"}},
			})
			if got != tc.existing || !errors.Is(err, tc.want) || lookups != 1 {
				t.Fatalf("Create() = (%v, %v), lookups = %d; want (%v, %v), one lookup", got, err, lookups, tc.existing, tc.want)
			}
		})
	}
}

func TestCreateParentLookupFailure(t *testing.T) {
	t.Parallel()

	lookupErr := errors.New("parent lookup failed")
	var calls []string
	ms := &mockStore{
		getByExternalIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) {
			calls = append(calls, "external ID")
			return nil, nil
		},
		getByIDFn: func(_ context.Context, id string) (*model.ChangeEvent, error) {
			calls = append(calls, "parent")
			if id != "parent-1" {
				t.Errorf("GetByID(%q), want parent-1", id)
			}
			return nil, lookupErr
		},
	}
	got, err := service.NewChangeService(ms).Create(t.Context(), &model.CreateChangeRequest{
		ExternalID: "new-event",
		ParentID:   "parent-1",
		UserName:   "alice",
		EventType:  model.EventTypeDeployment,
	})
	if got != nil || !errors.Is(err, lookupErr) {
		t.Fatalf("Create() = (%v, %v), want nil and parent lookup error", got, err)
	}
	if !slices.Equal(calls, []string{"external ID", "parent"}) {
		t.Errorf("store calls = %v, want external ID then parent", calls)
	}
}

func TestCreateOwnsMutableRequestFields(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		tags map[string]string
	}{
		{name: "nil tags"},
		{name: "empty tags", tags: map[string]string{}},
		{name: "normalized and free-form tags", tags: map[string]string{"team": " platform ", "severity": " SEV1 ", "custom": " unchanged "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := &model.CreateChangeRequest{
				UserName:  "alice",
				EventType: model.EventTypeDeployment,
				Tags:      tc.tags,
				Links:     []model.EventLink{{Label: "Plan", URL: "https://example.com/plan"}},
			}
			originalTags := maps.Clone(req.Tags)
			originalLinks := slices.Clone(req.Links)
			ms := &mockStore{createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) {
				return event, nil
			}}
			got, err := service.NewChangeService(ms).Create(t.Context(), req)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if got.Tags == nil {
				t.Fatal("created tags must be an initialized map")
			}
			if !maps.Equal(req.Tags, originalTags) || !slices.Equal(req.Links, originalLinks) {
				t.Fatal("Create() mutated the request")
			}
			if len(tc.tags) > 0 && (got.Tags["team"] != "platform" || got.Tags["severity"] != "sev1" || got.Tags["custom"] != " unchanged ") {
				t.Errorf("stored tags = %v, want normalized known tags and unchanged custom tag", got.Tags)
			}

			got.Tags["response-only"] = "value"
			got.Links[0].Label = "response-only"
			if !maps.Equal(req.Tags, originalTags) || !slices.Equal(req.Links, originalLinks) {
				t.Fatal("mutating the created event changed the request")
			}
			if req.Tags == nil {
				req.Tags = make(map[string]string)
			}
			req.Tags["request-only"] = "value"
			req.Links[0].URL = "https://example.com/changed"
			if _, ok := got.Tags["request-only"]; ok || got.Links[0].URL != originalLinks[0].URL {
				t.Fatal("mutating the request changed the created event")
			}
		})
	}
}

func TestCloseOperationBypassesNewEventNormalization(t *testing.T) {
	t.Parallel()

	start := &model.ChangeEvent{
		ID:        "legacy-start",
		EventType: " Maintenance ",
		Tags: map[string]string{
			"phase": "start", "change_id": " legacy-id ", "team": " platform ",
			"severity": "critical", "scope": "regional", "custom": " unchanged ",
		},
	}
	originalTags := maps.Clone(start.Tags)
	_, err := service.NewChangeService(&mockStore{}).Create(t.Context(), &model.CreateChangeRequest{
		UserName: "alice", EventType: start.EventType, Tags: start.Tags,
	})
	if !errors.Is(err, service.ErrInvalidTags) {
		t.Fatalf("Create() error = %v, want legacy tags rejected for a new event", err)
	}
	ms := &mockStore{
		getByIDFn: func(_ context.Context, _ string) (*model.ChangeEvent, error) { return start, nil },
		listCurrentFn: func(_ context.Context, params model.CurrentParams) (*model.ListResult, error) {
			if params.EventType != start.EventType || params.CorrelationValue != " legacy-id " {
				t.Errorf("closure lookup changed legacy identity: %+v", params)
			}
			return &model.ListResult{TotalCount: 1}, nil
		},
		createFn: func(_ context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error) { return event, nil },
	}
	closed, err := service.NewChangeService(ms).CloseOperation(t.Context(), start.ID, "alice", "finished")
	if err != nil {
		t.Fatalf("CloseOperation() error = %v", err)
	}
	wantTags := maps.Clone(originalTags)
	wantTags["phase"] = "end"
	if closed.EventType != start.EventType || !maps.Equal(closed.Tags, wantTags) {
		t.Fatalf("closure = type %q, tags %v; want type %q, tags %v", closed.EventType, closed.Tags, start.EventType, wantTags)
	}
	closed.Tags["change_id"] = "changed"
	if !maps.Equal(start.Tags, originalTags) {
		t.Fatalf("closing or mutating the closure changed start tags: %v", start.Tags)
	}
}
