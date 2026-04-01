//go:build integration

package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// testSchemaSQL contains the full schema for test databases.
// This mirrors what migrations 001 + 002 produce.
const testSchemaSQL = `
CREATE TABLE IF NOT EXISTS change_events (
	id               TEXT PRIMARY KEY,
	user_name        TEXT NOT NULL,
	timestamp_start  TEXT NOT NULL,
	timestamp_end    TEXT,
	event_type       TEXT NOT NULL DEFAULT '',
	description      TEXT NOT NULL DEFAULT '',
	long_description TEXT NOT NULL DEFAULT '',
	created_at       TEXT NOT NULL,
	updated_at       TEXT NOT NULL,
	starred          INTEGER NOT NULL DEFAULT 0,
	alerted          INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS change_event_tags (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id TEXT    NOT NULL REFERENCES change_events(id) ON DELETE CASCADE,
	key      TEXT    NOT NULL,
	value    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_change_event_tags_key_value ON change_event_tags (key, value);
CREATE INDEX IF NOT EXISTS idx_change_event_tags_event_id ON change_event_tags (event_id);
`

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := New(dbPath, 5*time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("newTestStore: %v", err)
	}
	if _, err := s.db.Exec(testSchemaSQL); err != nil {
		t.Fatalf("newTestStore schema: %v", err)
	}
	t.Cleanup(func() { s.Close() })
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
func strPtr(s string) *string         { return &s }

func makeEvent(id string, userName string, eventType string, start time.Time, tags map[string]string) *model.ChangeEvent {
	now := time.Now().UTC().Truncate(time.Second)
	return &model.ChangeEvent{
		ID:              id,
		UserName:        userName,
		TimestampStart:  start,
		EventType:       eventType,
		Description:     "desc-" + id,
		LongDescription: "long-desc-" + id,
		Tags:            tags,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

// ---------------------------------------------------------------------------
// Create tests
// ---------------------------------------------------------------------------

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("all fields populated", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		start := mustTime(t, "2026-01-15T10:00:00Z")
		end := mustTime(t, "2026-01-15T11:00:00Z")
		now := time.Now().UTC().Truncate(time.Second)

		ev := &model.ChangeEvent{
			ID:              "evt-001",
			UserName:        "alice",
			TimestampStart:  start,
			TimestampEnd:    &end,
			EventType:       model.EventTypeDeployment,
			Description:     "deploy v1.2.3",
			LongDescription: "Rolling deploy of service-foo to v1.2.3 across all regions",
			Tags:            map[string]string{"env": "prod", "service": "foo", "region": "us-east-1"},
			CreatedAt:       now,
			UpdatedAt:       now,
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
		if !got.TimestampStart.Equal(ev.TimestampStart) {
			t.Errorf("TimestampStart = %v, want %v", got.TimestampStart, ev.TimestampStart)
		}
		if got.TimestampEnd == nil {
			t.Fatal("TimestampEnd is nil, want non-nil")
		}
		if !got.TimestampEnd.Equal(end) {
			t.Errorf("TimestampEnd = %v, want %v", got.TimestampEnd, end)
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
		if len(got.Tags) != 3 {
			t.Fatalf("len(Tags) = %d, want 3", len(got.Tags))
		}
		for k, v := range ev.Tags {
			if got.Tags[k] != v {
				t.Errorf("Tags[%q] = %q, want %q", k, got.Tags[k], v)
			}
		}
	})

	t.Run("minimal fields", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		start := mustTime(t, "2026-02-01T09:00:00Z")
		now := time.Now().UTC().Truncate(time.Second)

		ev := &model.ChangeEvent{
			ID:             "evt-min",
			UserName:       "bob",
			TimestampStart: start,
			EventType:      model.EventTypeFeatureFlag,
			Description:    "toggle flag-x",
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if got.TimestampEnd != nil {
			t.Errorf("TimestampEnd = %v, want nil", got.TimestampEnd)
		}
		if got.LongDescription != "" {
			t.Errorf("LongDescription = %q, want empty", got.LongDescription)
		}
		if got.Tags == nil {
			// Tags can be nil when empty; that is acceptable.
		} else if len(got.Tags) != 0 {
			t.Errorf("len(Tags) = %d, want 0", len(got.Tags))
		}
	})
}

// ---------------------------------------------------------------------------
// GetByID tests
// ---------------------------------------------------------------------------

func TestGetByID(t *testing.T) {
	t.Parallel()

	t.Run("existing event with tags", func(t *testing.T) {
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
		if got.Tags["cluster"] != "prod-1" {
			t.Errorf("Tags[cluster] = %q, want %q", got.Tags["cluster"], "prod-1")
		}
	})

	t.Run("non-existent ID returns nil nil", func(t *testing.T) {
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
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestUpdate(t *testing.T) {
	t.Parallel()

	t.Run("update all fields", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		orig := makeEvent("upd-1", "dave", model.EventTypeDeployment, mustTime(t, "2026-01-10T12:00:00Z"), map[string]string{"env": "staging"})
		created, err := s.Create(ctx, orig)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		newEnd := mustTime(t, "2026-01-10T13:00:00Z")
		newStart := mustTime(t, "2026-01-10T12:30:00Z")
		updated := &model.ChangeEvent{
			ID:              created.ID,
			UserName:        "eve",
			TimestampStart:  newStart,
			TimestampEnd:    &newEnd,
			EventType:       model.EventTypeFeatureFlag,
			Description:     "updated desc",
			LongDescription: "updated long desc",
			Tags:            map[string]string{"env": "prod", "new-tag": "yes"},
			CreatedAt:       created.CreatedAt,
			UpdatedAt:       time.Now().UTC().Truncate(time.Second),
		}

		got, err := s.Update(ctx, updated)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}

		if got.UserName != "eve" {
			t.Errorf("UserName = %q, want %q", got.UserName, "eve")
		}
		if got.EventType != model.EventTypeFeatureFlag {
			t.Errorf("EventType = %q, want %q", got.EventType, model.EventTypeFeatureFlag)
		}
		if got.Description != "updated desc" {
			t.Errorf("Description = %q, want %q", got.Description, "updated desc")
		}
		if got.LongDescription != "updated long desc" {
			t.Errorf("LongDescription = %q, want %q", got.LongDescription, "updated long desc")
		}
		if !got.TimestampStart.Equal(newStart) {
			t.Errorf("TimestampStart = %v, want %v", got.TimestampStart, newStart)
		}
		if got.TimestampEnd == nil || !got.TimestampEnd.Equal(newEnd) {
			t.Errorf("TimestampEnd = %v, want %v", got.TimestampEnd, newEnd)
		}
	})

	t.Run("tags replaced on update", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("upd-tags", "frank", model.EventTypeDeployment, mustTime(t, "2026-02-20T10:00:00Z"), map[string]string{"old": "tag"})
		created, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		created.Tags = map[string]string{"new": "tag", "another": "value"}
		created.UpdatedAt = time.Now().UTC().Truncate(time.Second)

		got, err := s.Update(ctx, created)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}

		if len(got.Tags) != 2 {
			t.Fatalf("len(Tags) = %d, want 2", len(got.Tags))
		}
		if got.Tags["new"] != "tag" {
			t.Errorf("Tags[new] = %q, want %q", got.Tags["new"], "tag")
		}
		if _, ok := got.Tags["old"]; ok {
			t.Error("old tag still present after update")
		}
	})

	t.Run("updatedAt changes", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("upd-ts", "gina", model.EventTypeK8sChange, mustTime(t, "2026-03-01T06:00:00Z"), nil)
		created, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		newUpdatedAt := created.UpdatedAt.Add(5 * time.Minute).Truncate(time.Second)
		created.UpdatedAt = newUpdatedAt
		created.Description = "changed"

		got, err := s.Update(ctx, created)
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		if !got.UpdatedAt.Equal(newUpdatedAt) {
			t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, newUpdatedAt)
		}
	})
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestDelete(t *testing.T) {
	t.Parallel()

	t.Run("delete existing event", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("del-1", "hank", model.EventTypeDeployment, mustTime(t, "2026-01-05T15:00:00Z"), map[string]string{"a": "b"})
		if _, err := s.Create(ctx, ev); err != nil {
			t.Fatalf("Create: %v", err)
		}

		if err := s.Delete(ctx, "del-1"); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		got, err := s.GetByID(ctx, "del-1")
		if err != nil {
			t.Fatalf("GetByID after delete: %v", err)
		}
		if got != nil {
			t.Errorf("event still exists after delete: %+v", got)
		}
	})

	t.Run("delete non-existent event returns error", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		err := s.Delete(ctx, "no-such-id")
		if err == nil {
			t.Fatal("expected error deleting non-existent event, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %q, want it to contain 'not found'", err.Error())
		}
	})

	t.Run("tags cascade deleted", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "cascade.db")

		s, err := New(dbPath, 5*time.Second, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := s.db.Exec(testSchemaSQL); err != nil {
			t.Fatalf("schema: %v", err)
		}

		ctx := context.Background()
		ev := makeEvent("del-cascade", "ivy", model.EventTypeDeployment, mustTime(t, "2026-01-20T10:00:00Z"), map[string]string{"k1": "v1", "k2": "v2"})
		if _, err := s.Create(ctx, ev); err != nil {
			t.Fatalf("Create: %v", err)
		}

		if err := s.Delete(ctx, "del-cascade"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		s.Close()

		// Reopen the database and verify no orphaned tags remain.
		s2, err := New(dbPath, 5*time.Second, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer s2.Close()

		var count int
		err = s2.GetDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM change_event_tags WHERE event_id = ?`, "del-cascade").Scan(&count)
		if err != nil {
			t.Fatalf("count tags: %v", err)
		}
		if count != 0 {
			t.Errorf("orphaned tags count = %d, want 0", count)
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

	t.Run("all events no filters", func(t *testing.T) {
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
		// Events should be ordered by timestamp_start DESC.
		for i := 1; i < len(res.Events); i++ {
			if res.Events[i].TimestampStart.After(res.Events[i-1].TimestampStart) {
				t.Errorf("events not in descending order at index %d: %v > %v",
					i, res.Events[i].TimestampStart, res.Events[i-1].TimestampStart)
			}
		}
	})

	// Table-driven filter tests: each case seeds the same 5 events and
	// asserts the expected TotalCount for the given ListParams.
	filterCases := []struct {
		name           string
		params         model.ListParams
		expectedCount  int
		seed           bool // true = seed events, false = empty store
	}{
		{
			name:          "filter by StartAfter only",
			params:        model.ListParams{StartAfter: timePtr(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 3, // list-3, list-4, list-5
			seed:          true,
		},
		{
			name:          "filter by StartBefore only",
			params:        model.ListParams{StartBefore: timePtr(mustTime(t, "2026-01-03T00:00:00Z"))},
			expectedCount: 2, // list-1, list-2
			seed:          true,
		},
		{
			name: "filter by time range both",
			params: model.ListParams{
				StartAfter:  timePtr(mustTime(t, "2026-01-02T00:00:00Z")),
				StartBefore: timePtr(mustTime(t, "2026-01-04T10:00:00Z")),
			},
			expectedCount: 2, // list-2, list-3
			seed:          true,
		},
		{
			name:          "filter by UserName",
			params:        model.ListParams{UserName: "alice"},
			expectedCount: 3, // list-1, list-3, list-5
			seed:          true,
		},
		{
			name:          "filter by EventType",
			params:        model.ListParams{EventType: model.EventTypeDeployment},
			expectedCount: 3, // list-1, list-4, list-5
			seed:          true,
		},
		{
			name:          "filter by single tag",
			params:        model.ListParams{Tags: map[string]string{"env": "prod"}},
			expectedCount: 3, // list-1, list-2, list-4
			seed:          true,
		},
		{
			name:          "filter by multiple tags AND logic",
			params:        model.ListParams{Tags: map[string]string{"env": "prod", "service": "api"}},
			expectedCount: 1, // list-1 only
			seed:          true,
		},
		{
			name:          "combined filters user and type",
			params:        model.ListParams{UserName: "alice", EventType: model.EventTypeDeployment},
			expectedCount: 2, // list-1, list-5
			seed:          true,
		},
		{
			name:          "empty results with filters",
			params:        model.ListParams{UserName: "nonexistent-user"},
			expectedCount: 0,
			seed:          true,
		},
	}
	for _, tc := range filterCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if tc.seed {
				seedEvents(t, s)
			}
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

	t.Run("pagination limit and offset", func(t *testing.T) {
		s := newTestStore(t)
		seedEvents(t, s)
		ctx := context.Background()

		// Page 1: first 2 events (descending by start time: list-5, list-4)
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
		if res2.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res2.TotalCount)
		}
		if len(res2.Events) != 2 {
			t.Fatalf("len(Events) = %d, want 2", len(res2.Events))
		}

		// Page 3: last 1
		res3, err := s.List(ctx, model.ListParams{Limit: 2, Offset: 4})
		if err != nil {
			t.Fatalf("List page 3: %v", err)
		}
		if res3.TotalCount != 5 {
			t.Errorf("TotalCount = %d, want 5", res3.TotalCount)
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

	t.Run("empty results", func(t *testing.T) {
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

		// list-1 has 2 tags, list-2 has 1, list-3 has 2, list-4 has 2, list-5 has 0
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
// Edge cases
// ---------------------------------------------------------------------------

func TestEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("empty tags map", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("edge-empty-tags", "user1", model.EventTypeDeployment, mustTime(t, "2026-03-10T10:00:00Z"), map[string]string{})
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		// An empty tags map can come back as nil or empty; both are fine.
		if got.Tags != nil && len(got.Tags) != 0 {
			t.Errorf("len(Tags) = %d, want 0", len(got.Tags))
		}
	})

	t.Run("many tags", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		tags := make(map[string]string)
		for i := 0; i < 50; i++ {
			tags[fmt.Sprintf("key-%03d", i)] = fmt.Sprintf("val-%03d", i)
		}

		ev := makeEvent("edge-many-tags", "user2", model.EventTypeFeatureFlag, mustTime(t, "2026-03-11T10:00:00Z"), tags)
		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if len(got.Tags) != 50 {
			t.Errorf("len(Tags) = %d, want 50", len(got.Tags))
		}
		for k, v := range tags {
			if got.Tags[k] != v {
				t.Errorf("Tags[%q] = %q, want %q", k, got.Tags[k], v)
			}
		}
	})

	t.Run("unicode in descriptions", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		ev := makeEvent("edge-unicode", "user3", model.EventTypeK8sChange, mustTime(t, "2026-03-12T10:00:00Z"), nil)
		ev.Description = "Deploying \u2192 v2.0 \U0001F680"
		ev.LongDescription = "\u65e5\u672c\u8a9e\u306e\u8aac\u660e\u3002 \u041e\u043f\u0438\u0441\u0430\u043d\u0438\u0435 \u043d\u0430 \u0440\u0443\u0441\u0441\u043a\u043e\u043c. \u4e2d\u6587\u63cf\u8ff0\u3002"

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got.Description != ev.Description {
			t.Errorf("Description = %q, want %q", got.Description, ev.Description)
		}
		if got.LongDescription != ev.LongDescription {
			t.Errorf("LongDescription = %q, want %q", got.LongDescription, ev.LongDescription)
		}

		// Also verify via GetByID roundtrip.
		fetched, err := s.GetByID(ctx, "edge-unicode")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if fetched.Description != ev.Description {
			t.Errorf("fetched Description = %q, want %q", fetched.Description, ev.Description)
		}
		if fetched.LongDescription != ev.LongDescription {
			t.Errorf("fetched LongDescription = %q, want %q", fetched.LongDescription, ev.LongDescription)
		}
	})

	t.Run("very long descriptions", func(t *testing.T) {
		s := newTestStore(t)
		ctx := context.Background()

		longDesc := strings.Repeat("a]", 50000) // 100k characters
		ev := makeEvent("edge-long", "user4", model.EventTypeDeployment, mustTime(t, "2026-03-13T10:00:00Z"), nil)
		ev.LongDescription = longDesc

		got, err := s.Create(ctx, ev)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if got.LongDescription != longDesc {
			t.Errorf("LongDescription length = %d, want %d", len(got.LongDescription), len(longDesc))
		}
	})
}
