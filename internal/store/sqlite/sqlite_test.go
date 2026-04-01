//go:build integration

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
	"github.com/sarah/go-prod-change-registry/migrations"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test infrastructure
// ---------------------------------------------------------------------------

// applyTestMigrations reads and executes the embedded migration SQL files in order.
func applyTestMigrations(t *testing.T, db *sql.DB) {
	t.Helper()
	migrationFiles := []string{
		"001_create_change_events.up.sql",
		"002_add_external_id.up.sql",
	}
	for _, name := range migrationFiles {
		sqlBytes, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

func openTestDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)",
		dbPath,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	return db
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db := openTestDB(t, dbPath)
	applyTestMigrations(t, db)
	s := New(db, 100*time.Millisecond)
	t.Cleanup(func() { db.Close() })
	return s
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("mustTime: %v", err)
	}
	return ts
}

func timePtr(ts time.Time) *time.Time { return &ts }

func durationPtr(d time.Duration) *time.Duration { return &d }

func makeEvent(id, userName, eventType string, ts time.Time, tags map[string]string) *model.ChangeEvent {
	return &model.ChangeEvent{
		ID:              id,
		UserName:        userName,
		Timestamp:       ts,
		EventType:       eventType,
		Description:     "desc-" + id,
		LongDescription: "long-desc-" + id,
		Tags:            tags,
		CreatedAt:       ts,
	}
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("event with all fields", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-01-15T10:00:00Z")
		ev := &model.ChangeEvent{
			ID:              "evt-001",
			UserName:        "alice",
			Timestamp:       ts,
			EventType:       model.EventTypeDeployment,
			Description:     "deploy v1.2.3",
			LongDescription: "Rolling deploy of service-foo to v1.2.3",
			Tags:            map[string]string{"env": "prod", "service": "foo", "region": "us-east-1"},
			CreatedAt:       ts,
		}

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if got.ID != ev.ID {
			t.Errorf("ID = %q, want %q", got.ID, ev.ID)
		}
		if got.UserName != ev.UserName {
			t.Errorf("UserName = %q, want %q", got.UserName, ev.UserName)
		}
		if !got.Timestamp.Equal(ev.Timestamp) {
			t.Errorf("Timestamp = %v, want %v", got.Timestamp, ev.Timestamp)
		}
		if got.EventType != ev.EventType {
			t.Errorf("EventType = %q, want %q", got.EventType, ev.EventType)
		}
		if got.Description != ev.Description {
			t.Errorf("Description = %q, want %q", got.Description, ev.Description)
		}
		if got.LongDescription != ev.LongDescription {
			t.Errorf("LongDescription = %q, want %q", got.LongDescription, ev.LongDescription)
		}
		if got.ParentID != "" {
			t.Errorf("ParentID = %q, want empty", got.ParentID)
		}
		if len(got.Tags) != 3 {
			t.Fatalf("len(Tags) = %d, want 3", len(got.Tags))
		}
		for k, v := range ev.Tags {
			if got.Tags[k] != v {
				t.Errorf("Tags[%q] = %q, want %q", k, got.Tags[k], v)
			}
		}
	})

	t.Run("event with tags", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-01T09:00:00Z")
		ev := makeEvent("evt-tags", "bob", model.EventTypeFeatureFlag, ts, map[string]string{"flag": "dark-mode", "team": "frontend"})

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(got.Tags) != 2 {
			t.Fatalf("len(Tags) = %d, want 2", len(got.Tags))
		}
		if got.Tags["flag"] != "dark-mode" {
			t.Errorf("Tags[flag] = %q, want %q", got.Tags["flag"], "dark-mode")
		}
		if got.Tags["team"] != "frontend" {
			t.Errorf("Tags[team] = %q, want %q", got.Tags["team"], "frontend")
		}
	})

	t.Run("meta-event with parent_id", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-01T10:00:00Z")
		parent := makeEvent("parent-1", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "star-1",
			ParentID:  "parent-1",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		got, err := s.Create(ctx, meta)
		if err != nil {
			t.Fatalf("Create meta: %v", err)
		}
		if got.ParentID != "parent-1" {
			t.Errorf("ParentID = %q, want %q", got.ParentID, "parent-1")
		}
		if got.EventType != model.EventTypeStar {
			t.Errorf("EventType = %q, want %q", got.EventType, model.EventTypeStar)
		}
		if !got.IsMetaEvent() {
			t.Error("IsMetaEvent() = false, want true")
		}
	})

	t.Run("minimal fields no tags", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-15T09:00:00Z")
		ev := &model.ChangeEvent{
			ID:        "evt-min",
			UserName:  "carol",
			Timestamp: ts,
			EventType: model.EventTypeK8sChange,
			CreatedAt: ts,
		}
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got.LongDescription != "" {
			t.Errorf("LongDescription = %q, want empty", got.LongDescription)
		}
		// Tags can be nil or empty when none set.
		if got.Tags != nil && len(got.Tags) != 0 {
			t.Errorf("len(Tags) = %d, want 0", len(got.Tags))
		}
	})
}

// ---------------------------------------------------------------------------
// ExternalID / idempotency tests
// ---------------------------------------------------------------------------

func TestCreateExternalID(t *testing.T) {
	t.Parallel()

	t.Run("duplicate external_id returns existing event", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), map[string]string{"env": "prod"})
		ev.ExternalID = "gh-actions-run-123"

		created, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		// Create again with same external_id but different ID.
		ev2 := makeEvent("ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev2.ExternalID = "gh-actions-run-123"

		duplicate, err := s.Create(ctx, ev2)
		if !errors.Is(err, store.ErrDuplicate) {
			t.Fatalf("expected store.ErrDuplicate, got %v", err)
		}
		if duplicate == nil {
			t.Fatal("expected existing event to be returned alongside ErrDuplicate")
		}
		if duplicate.ID != created.ID {
			t.Errorf("duplicate.ID = %q, want %q (original)", duplicate.ID, created.ID)
		}
		if duplicate.ExternalID != "gh-actions-run-123" {
			t.Errorf("duplicate.ExternalID = %q, want %q", duplicate.ExternalID, "gh-actions-run-123")
		}
	})

	t.Run("different external_ids create separate events", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev1 := makeEvent("diff-ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev1.ExternalID = "run-aaa"

		ev2 := makeEvent("diff-ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		ev2.ExternalID = "run-bbb"

		created1, err := s.Create(ctx, ev1)
		if err != nil {
			t.Fatalf("Create ev1: %v", err)
		}
		created2, err := s.Create(ctx, ev2)
		if err != nil {
			t.Fatalf("Create ev2: %v", err)
		}

		if created1.ID == created2.ID {
			t.Errorf("expected different IDs, both got %q", created1.ID)
		}
	})

	t.Run("empty external_id does not conflict", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev1 := makeEvent("empty-ext-1", "alice", model.EventTypeDeployment, time.Now().UTC(), nil)
		// ExternalID left empty (zero value).

		ev2 := makeEvent("empty-ext-2", "bob", model.EventTypeDeployment, time.Now().UTC(), nil)
		// ExternalID left empty (zero value).

		_, err := s.Create(ctx, ev1)
		if err != nil {
			t.Fatalf("Create ev1: %v", err)
		}
		_, err = s.Create(ctx, ev2)
		if err != nil {
			t.Fatalf("Create ev2: %v (empty external_id should not conflict)", err)
		}
	})

	t.Run("external_id is returned in GetByID", func(t *testing.T) {
		t.Parallel()

		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("ext-get-1", "carol", model.EventTypeK8sChange, time.Now().UTC(), nil)
		ev.ExternalID = "jenkins-build-42"

		_, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := s.GetByID(ctx, "ext-get-1")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ExternalID != "jenkins-build-42" {
			t.Errorf("ExternalID = %q, want %q", got.ExternalID, "jenkins-build-42")
		}
	})
}

// ---------------------------------------------------------------------------
// GetByID tests
// ---------------------------------------------------------------------------

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("existing event", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("get-1", "carol", model.EventTypeK8sChange, mustTime(t, "2026-03-01T08:00:00Z"), map[string]string{"cluster": "prod-1"})
		if _, err := s.Create(ctx, ev); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := s.GetByID(ctx, "get-1")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ID != "get-1" {
			t.Errorf("ID = %q, want %q", got.ID, "get-1")
		}
		if got.UserName != "carol" {
			t.Errorf("UserName = %q, want %q", got.UserName, "carol")
		}
		if got.Tags["cluster"] != "prod-1" {
			t.Errorf("Tags[cluster] = %q, want %q", got.Tags["cluster"], "prod-1")
		}
	})

	t.Run("non-existent returns nil", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		got, err := s.GetByID(ctx, "does-not-exist")
		if err != nil {
			t.Fatalf("GetByID error: %v", err)
		}
		if got != nil {
			t.Errorf("got %+v, want nil", got)
		}
	})

	t.Run("event with parent_id", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-05T10:00:00Z")
		parent := makeEvent("parent-get", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "meta-get",
			ParentID:  "parent-get",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, meta); err != nil {
			t.Fatalf("Create meta: %v", err)
		}

		got, err := s.GetByID(ctx, "meta-get")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got == nil {
			t.Fatal("GetByID returned nil")
		}
		if got.ParentID != "parent-get" {
			t.Errorf("ParentID = %q, want %q", got.ParentID, "parent-get")
		}
	})
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

// seedEvents inserts a known set of events for list tests and returns them.
func seedEvents(t *testing.T, s *Store) []model.ChangeEvent {
	t.Helper()
	ctx := context.Background()

	events := []*model.ChangeEvent{
		makeEvent("list-1", "alice", model.EventTypeDeployment, mustTime(t, "2026-01-01T10:00:00Z"), map[string]string{"env": "prod", "service": "api"}),
		makeEvent("list-2", "bob", model.EventTypeFeatureFlag, mustTime(t, "2026-01-02T10:00:00Z"), map[string]string{"env": "prod"}),
		makeEvent("list-3", "alice", model.EventTypeK8sChange, mustTime(t, "2026-01-03T10:00:00Z"), map[string]string{"env": "staging", "cluster": "us-west"}),
		makeEvent("list-4", "carol", model.EventTypeDeployment, mustTime(t, "2026-01-04T10:00:00Z"), map[string]string{"env": "prod", "service": "web"}),
		makeEvent("list-5", "alice", model.EventTypeDeployment, mustTime(t, "2026-01-05T10:00:00Z"), nil),
	}

	var created []model.ChangeEvent
	for _, ev := range events {
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("seed Create(%s): %v", ev.ID, err)
		}
		created = append(created, *got)
	}
	return created
}

func TestList(t *testing.T) {
	t.Parallel()

	t.Run("no filters returns all", func(t *testing.T) {
		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if res.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res.TotalCount)
		}
		if len(res.Events) != 5 {
			t.Errorf("len(Events) = %d, want 5", len(res.Events))
		}
		// Events should be ordered by timestamp DESC.
		for i := 1; i < len(res.Events); i++ {
			if res.Events[i].Timestamp.After(res.Events[i-1].Timestamp) {
				t.Errorf("events not in descending order at index %d: %v > %v",
					i, res.Events[i].Timestamp, res.Events[i-1].Timestamp)
			}
		}
	})

	// Table-driven filter tests.
	filterCases := []struct {
		name          string
		params        model.ListParams
		expectedCount int
	}{
		{
			name:          "filter by StartAfter only",
			params:        model.ListParams{StartAfter: timePtr(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 3, // list-3, list-4, list-5
		},
		{
			name:          "filter by StartBefore only",
			params:        model.ListParams{StartBefore: timePtr(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 2, // list-1, list-2
		},
		{
			name: "filter by time range both",
			params: model.ListParams{
				StartAfter:  timePtr(mustTime(t, "2026-01-02T00:00:00Z")),
				StartBefore: timePtr(mustTime(t, "2026-01-04T10:00:00Z")),
			},
			expectedCount: 2, // list-2, list-3
		},
		{
			name:          "filter by UserName",
			params:        model.ListParams{UserName: "alice"},
			expectedCount: 3, // list-1, list-3, list-5
		},
		{
			name:          "filter by EventType",
			params:        model.ListParams{EventType: model.EventTypeDeployment},
			expectedCount: 3, // list-1, list-4, list-5
		},
		{
			name:          "filter by single tag",
			params:        model.ListParams{Tags: map[string]string{"env": "prod"}},
			expectedCount: 3, // list-1, list-2, list-4
		},
		{
			name:          "filter by multiple tags AND logic",
			params:        model.ListParams{Tags: map[string]string{"env": "prod", "service": "api"}},
			expectedCount: 1, // list-1 only
		},
		{
			name:          "combined filters user and type",
			params:        model.ListParams{UserName: "alice", EventType: model.EventTypeDeployment},
			expectedCount: 2, // list-1, list-5
		},
		{
			name:          "empty results with filters",
			params:        model.ListParams{UserName: "nonexistent-user"},
			expectedCount: 0,
		},
	}
	for _, tc := range filterCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedEvents(t, s)
			ctx := context.Background()

			res, err := s.List(ctx, tc.params)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if res.TotalCount != tc.expectedCount {
				t.Errorf("TotalCount = %d, want %d", res.TotalCount, tc.expectedCount)
			}
		})
	}

	t.Run("TopLevel excludes meta-events", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-02-01T10:00:00Z")
		parent := makeEvent("top-1", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		meta := &model.ChangeEvent{
			ID:        "meta-top",
			ParentID:  "top-1",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, meta); err != nil {
			t.Fatalf("Create meta: %v", err)
		}

		// Without TopLevel: both events.
		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List all: %v", err)
		}
		if res.TotalCount != 2 {
			t.Errorf("TotalCount (all) = %d, want 2", res.TotalCount)
		}

		// With TopLevel: only the parent.
		res, err = s.List(ctx, model.ListParams{TopLevel: true})
		if err != nil {
			t.Fatalf("List TopLevel: %v", err)
		}
		if res.TotalCount != 1 {
			t.Errorf("TotalCount (TopLevel) = %d, want 1", res.TotalCount)
		}
		if len(res.Events) != 1 {
			t.Fatalf("len(Events) = %d, want 1", len(res.Events))
		}
		if res.Events[0].ID != "top-1" {
			t.Errorf("Events[0].ID = %q, want %q", res.Events[0].ID, "top-1")
		}
	})

	t.Run("pagination limit and offset", func(t *testing.T) {
		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		// Page 1: first 2 events (descending by timestamp: list-5, list-4)
		res, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("List page 1: %v", err)
		}
		if res.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res.TotalCount)
		}
		if len(res.Events) != 2 {
			t.Fatalf("len(Events) = %d, want 2", len(res.Events))
		}
		if res.Limit != 2 {
			t.Errorf("Limit = %d, want 2", res.Limit)
		}
		if res.Offset != 0 {
			t.Errorf("Offset = %d, want 0", res.Offset)
		}

		// Page 2: next 2
		res2, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("List page 2: %v", err)
		}
		if len(res2.Events) != 2 {
			t.Fatalf("len(Events) = %d, want 2", len(res2.Events))
		}

		// Page 3: last 1
		res3, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 4})
		if err != nil {
			t.Fatalf("List page 3: %v", err)
		}
		if len(res3.Events) != 1 {
			t.Fatalf("len(Events) = %d, want 1", len(res3.Events))
		}

		// Ensure no overlapping IDs across pages.
		seen := make(map[string]bool)
		for _, ev := range res.Events {
			seen[ev.ID] = true
		}
		for _, ev := range res2.Events {
			if seen[ev.ID] {
				t.Errorf("duplicate event %q across pages", ev.ID)
			}
			seen[ev.ID] = true
		}
		for _, ev := range res3.Events {
			if seen[ev.ID] {
				t.Errorf("duplicate event %q across pages", ev.ID)
			}
		}
	})

	t.Run("Around and Window query", func(t *testing.T) {
		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		// Around 2026-01-03T10:00:00Z with a 24h window should include
		// events within [2026-01-02T10:00:00Z, 2026-01-04T10:00:00Z).
		around := mustTime(t, "2026-01-03T10:00:00Z")
		res, err := s.List(ctx, model.ListParams{
			Around: timePtr(around),
			Window: durationPtr(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("List Around: %v", err)
		}
		// list-2 (Jan 2 10:00), list-3 (Jan 3 10:00) fall within the window.
		// list-4 (Jan 4 10:00) is at the boundary (exclusive end), so excluded.
		if res.TotalCount != 2 {
			t.Errorf("TotalCount = %d, want 2", res.TotalCount)
		}
	})

	t.Run("empty store returns empty slice", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if res.TotalCount != 0 {
			t.Errorf("TotalCount = %d, want 0", res.TotalCount)
		}
		if res.Events == nil {
			t.Error("Events is nil, want empty slice")
		}
		if len(res.Events) != 0 {
			t.Errorf("len(Events) = %d, want 0", len(res.Events))
		}
	})

	t.Run("events have correct tags loaded", func(t *testing.T) {
		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		res, err := s.List(ctx, model.ListParams{Limit: 50})
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		tagCounts := make(map[string]int)
		for _, ev := range res.Events {
			tagCounts[ev.ID] = len(ev.Tags)
		}

		expected := map[string]int{
			"list-1": 2,
			"list-2": 1,
			"list-3": 2,
			"list-4": 2,
			"list-5": 0,
		}
		for id, want := range expected {
			if tagCounts[id] != want {
				t.Errorf("event %s tag count = %d, want %d", id, tagCounts[id], want)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// GetAnnotations tests
// ---------------------------------------------------------------------------

func TestGetAnnotations(t *testing.T) {
	t.Parallel()

	t.Run("no annotations returns defaults", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-none", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-none")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if ann.Starred {
			t.Error("Starred = true, want false")
		}
		if ann.Alerted {
			t.Error("Alerted = true, want false")
		}
	})

	t.Run("star then check Starred is true", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-star", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-star-meta",
			ParentID:  "ann-star",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-star")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Starred {
			t.Error("Starred = false, want true")
		}
		if ann.Alerted {
			t.Error("Alerted = true, want false")
		}
	})

	t.Run("star then unstar returns Starred false", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-unstar", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-unstar-star",
			ParentID:  "ann-unstar",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		unstarEvt := &model.ChangeEvent{
			ID:        "ann-unstar-unstar",
			ParentID:  "ann-unstar",
			UserName:  "bob",
			Timestamp: ts.Add(2 * time.Minute),
			EventType: model.EventTypeUnstar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(2 * time.Minute),
		}
		if _, err := s.Create(ctx, unstarEvt); err != nil {
			t.Fatalf("Create unstar: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-unstar")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if ann.Starred {
			t.Error("Starred = true, want false after unstar")
		}
	})

	t.Run("alert then check Alerted is true", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-alert", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		alertEvt := &model.ChangeEvent{
			ID:        "ann-alert-meta",
			ParentID:  "ann-alert",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-alert")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Alerted {
			t.Error("Alerted = false, want true")
		}
		if ann.Starred {
			t.Error("Starred = true, want false")
		}
	})

	t.Run("both star and alert simultaneously", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-10T10:00:00Z")
		parent := makeEvent("ann-both", "alice", model.EventTypeDeployment, ts, nil)
		if _, err := s.Create(ctx, parent); err != nil {
			t.Fatalf("Create parent: %v", err)
		}

		starEvt := &model.ChangeEvent{
			ID:        "ann-both-star",
			ParentID:  "ann-both",
			UserName:  "bob",
			Timestamp: ts.Add(time.Minute),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(time.Minute),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		alertEvt := &model.ChangeEvent{
			ID:        "ann-both-alert",
			ParentID:  "ann-both",
			UserName:  "carol",
			Timestamp: ts.Add(2 * time.Minute),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(2 * time.Minute),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		ann, err := s.GetAnnotations(ctx, "ann-both")
		if err != nil {
			t.Fatalf("GetAnnotations: %v", err)
		}
		if !ann.Starred {
			t.Error("Starred = false, want true")
		}
		if !ann.Alerted {
			t.Error("Alerted = false, want true")
		}
	})
}

// ---------------------------------------------------------------------------
// GetAnnotationsBatch tests
// ---------------------------------------------------------------------------

func TestGetAnnotationsBatch(t *testing.T) {
	t.Parallel()

	t.Run("multiple events with different annotations", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ts := mustTime(t, "2026-03-15T10:00:00Z")

		// Create three parent events.
		ev1 := makeEvent("batch-1", "alice", model.EventTypeDeployment, ts, nil)
		ev2 := makeEvent("batch-2", "bob", model.EventTypeDeployment, ts.Add(time.Hour), nil)
		ev3 := makeEvent("batch-3", "carol", model.EventTypeDeployment, ts.Add(2*time.Hour), nil)
		for _, ev := range []*model.ChangeEvent{ev1, ev2, ev3} {
			if _, err := s.Create(ctx, ev); err != nil {
				t.Fatalf("Create %s: %v", ev.ID, err)
			}
		}

		// Star batch-1.
		starEvt := &model.ChangeEvent{
			ID:        "batch-1-star",
			ParentID:  "batch-1",
			UserName:  "bob",
			Timestamp: ts.Add(3 * time.Hour),
			EventType: model.EventTypeStar,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(3 * time.Hour),
		}
		if _, err := s.Create(ctx, starEvt); err != nil {
			t.Fatalf("Create star: %v", err)
		}

		// Alert batch-2.
		alertEvt := &model.ChangeEvent{
			ID:        "batch-2-alert",
			ParentID:  "batch-2",
			UserName:  "carol",
			Timestamp: ts.Add(4 * time.Hour),
			EventType: model.EventTypeAlert,
			Tags:      map[string]string{},
			CreatedAt: ts.Add(4 * time.Hour),
		}
		if _, err := s.Create(ctx, alertEvt); err != nil {
			t.Fatalf("Create alert: %v", err)
		}

		// batch-3 has no annotations.

		result, err := s.GetAnnotationsBatch(ctx, []string{"batch-1", "batch-2", "batch-3"})
		if err != nil {
			t.Fatalf("GetAnnotationsBatch: %v", err)
		}

		if len(result) != 3 {
			t.Fatalf("len(result) = %d, want 3", len(result))
		}

		// batch-1: starred, not alerted.
		if !result["batch-1"].Starred {
			t.Error("batch-1 Starred = false, want true")
		}
		if result["batch-1"].Alerted {
			t.Error("batch-1 Alerted = true, want false")
		}

		// batch-2: not starred, alerted.
		if result["batch-2"].Starred {
			t.Error("batch-2 Starred = true, want false")
		}
		if !result["batch-2"].Alerted {
			t.Error("batch-2 Alerted = false, want true")
		}

		// batch-3: neither starred nor alerted.
		if result["batch-3"].Starred {
			t.Error("batch-3 Starred = true, want false")
		}
		if result["batch-3"].Alerted {
			t.Error("batch-3 Alerted = true, want false")
		}
	})

	t.Run("empty input returns empty map", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		result, err := s.GetAnnotationsBatch(ctx, []string{})
		if err != nil {
			t.Fatalf("GetAnnotationsBatch: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("len(result) = %d, want 0", len(result))
		}
	})
}
