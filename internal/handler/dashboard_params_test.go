package handler

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// White-box tests for the dashboard query-parameter parser and the
// event+annotation merger. Covers the three gaps the refactor review
// called out: around/window interactions (via list_params_test),
// silent fallback on unparseable custom-range datetimes, and
// nil annotation entries in buildDashboardEvents.

func TestParseDashboardRequest(t *testing.T) {
	t.Parallel()

	t.Run("default query sets TopLevel and 24h range", func(t *testing.T) {
		t.Parallel()

		r := httptest.NewRequest("GET", "/", nil)
		p, f := parseDashboardRequest(r)

		if !p.TopLevel {
			t.Error("expected TopLevel = true by default")
		}
		if f.Range != "24h" {
			t.Errorf("filters.Range = %q, want 24h", f.Range)
		}
		if p.StartAfter == nil {
			t.Fatal("expected StartAfter to be set for default 24h range")
		}
		if p.Limit != model.DashboardLimit {
			t.Errorf("Limit = %d, want DashboardLimit %d", p.Limit, model.DashboardLimit)
		}
		if p.Offset != 0 {
			t.Errorf("Offset = %d, want 0", p.Offset)
		}
	})

	t.Run("full custom filter populates params and echoes filters", func(t *testing.T) {
		t.Parallel()

		r := httptest.NewRequest(
			"GET",
			"/?range=custom"+
				"&start_after=2026-04-20T10:00"+
				"&start_before=2026-04-25T18:00"+
				"&alerted=true"+
				"&type=deploy"+
				"&user=alice"+
				"&tag=env:prod"+
				"&tag=tier:1"+
				"&limit=25"+
				"&offset=50",
			nil,
		)
		p, f := parseDashboardRequest(r)

		if f.Range != "custom" {
			t.Errorf("filters.Range = %q, want custom", f.Range)
		}
		if f.StartAfter != "2026-04-20T10:00" || f.StartBefore != "2026-04-25T18:00" {
			t.Errorf("filter datetimes not echoed: %q / %q", f.StartAfter, f.StartBefore)
		}
		if !f.Alerted || !p.AlertedOnly {
			t.Errorf("Alerted/AlertedOnly = %v/%v, want true/true", f.Alerted, p.AlertedOnly)
		}
		if p.EventType != "deploy" || p.UserName != "alice" {
			t.Errorf("EventType/UserName = %q/%q", p.EventType, p.UserName)
		}
		if p.Limit != 25 || p.Offset != 50 {
			t.Errorf("Limit/Offset = %d/%d, want 25/50", p.Limit, p.Offset)
		}
		if p.Tags["env"] != "prod" || p.Tags["tier"] != "1" {
			t.Errorf("tags map = %v", p.Tags)
		}
		if len(f.Tags) != 2 {
			t.Errorf("filters.Tags len = %d, want 2", len(f.Tags))
		}
	})
}

func TestParseDashboardRange(t *testing.T) {
	t.Parallel()

	t.Run("empty range defaults to 24h", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		before := time.Now().UTC()
		parseDashboardRange(url.Values{}, &p, &f)
		after := time.Now().UTC()

		if f.Range != "24h" {
			t.Errorf("Range = %q, want 24h", f.Range)
		}
		if p.StartAfter == nil {
			t.Fatal("expected StartAfter to be set")
		}
		// StartAfter should be roughly 24h before "now" (within the test window).
		wantLow := before.Add(-24 * time.Hour)
		wantHigh := after.Add(-24 * time.Hour)
		if p.StartAfter.Before(wantLow) || p.StartAfter.After(wantHigh) {
			t.Errorf("StartAfter = %v, want within [%v, %v]", p.StartAfter, wantLow, wantHigh)
		}
	})

	t.Run("quick-select range uses the matching duration", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			rangeVal string
			dur      time.Duration
		}{
			{"5m", 5 * time.Minute},
			{"30m", 30 * time.Minute},
			{"1h", time.Hour},
			{"24h", 24 * time.Hour},
		} {
			var p model.ListParams
			var f dashboardFilters
			parseDashboardRange(url.Values{"range": {tc.rangeVal}}, &p, &f)

			if f.Range != tc.rangeVal {
				t.Errorf("Range = %q, want %q", f.Range, tc.rangeVal)
			}
			if p.StartAfter == nil {
				t.Fatalf("%s: StartAfter is nil", tc.rangeVal)
			}
			// Confirm StartAfter is within a minute of (now - dur).
			expected := time.Now().UTC().Add(-tc.dur)
			delta := expected.Sub(*p.StartAfter)
			if delta < -time.Minute || delta > time.Minute {
				t.Errorf("%s: StartAfter off by %v", tc.rangeVal, delta)
			}
		}
	})

	t.Run("unrecognized range is stored but sets no time bounds", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardRange(url.Values{"range": {"foo"}}, &p, &f)

		if f.Range != "foo" {
			t.Errorf("Range = %q, want foo (echoed back)", f.Range)
		}
		if p.StartAfter != nil || p.StartBefore != nil {
			t.Errorf("expected no time bounds for unrecognized range, got %+v / %+v", p.StartAfter, p.StartBefore)
		}
	})

	t.Run("custom range with valid datetimes sets both bounds", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardRange(
			url.Values{
				"range":        {"custom"},
				"start_after":  {"2026-04-20T10:00"},
				"start_before": {"2026-04-25T18:00"},
			},
			&p, &f,
		)

		if p.StartAfter == nil || !p.StartAfter.Equal(time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)) {
			t.Errorf("StartAfter = %v, want 2026-04-20T10:00", p.StartAfter)
		}
		if p.StartBefore == nil || !p.StartBefore.Equal(time.Date(2026, 4, 25, 18, 0, 0, 0, time.UTC)) {
			t.Errorf("StartBefore = %v, want 2026-04-25T18:00", p.StartBefore)
		}
	})

	// This is the branch the refactor review called out: the dashboard is
	// deliberately lenient about bad datetime input — the raw value is
	// echoed to the form so the user can fix their typo, but params
	// remain unset so the query just falls back to an open range.
	t.Run("custom range with unparseable start_after echoes raw and leaves params nil", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardRange(
			url.Values{
				"range":       {"custom"},
				"start_after": {"not-a-datetime"},
			},
			&p, &f,
		)

		if f.StartAfter != "not-a-datetime" {
			t.Errorf("filters.StartAfter = %q, expected raw echo", f.StartAfter)
		}
		if p.StartAfter != nil {
			t.Errorf("params.StartAfter = %v, expected nil on parse failure", p.StartAfter)
		}
	})

	t.Run("custom range with unparseable start_before echoes raw and leaves params nil", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardRange(
			url.Values{
				"range":        {"custom"},
				"start_before": {"garbage"},
			},
			&p, &f,
		)

		if f.StartBefore != "garbage" {
			t.Errorf("filters.StartBefore = %q, expected raw echo", f.StartBefore)
		}
		if p.StartBefore != nil {
			t.Errorf("params.StartBefore = %v, expected nil on parse failure", p.StartBefore)
		}
	})
}

func TestParseDashboardTags(t *testing.T) {
	t.Parallel()

	t.Run("malformed entries are silently skipped", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardTags(
			url.Values{"tag": {"env:prod", "malformed", ":empty-key", "tier:1"}},
			&p, &f,
		)

		if p.Tags["env"] != "prod" || p.Tags["tier"] != "1" {
			t.Errorf("params.Tags = %v, want env=prod tier=1", p.Tags)
		}
		if len(p.Tags) != 2 {
			t.Errorf("expected 2 valid tags, got %d", len(p.Tags))
		}
		// filters.Tags should only echo successfully parsed entries, since
		// they are displayed as filter chips on the rendered page.
		if len(f.Tags) != 2 {
			t.Errorf("filters.Tags = %v, want only the two valid entries", f.Tags)
		}
	})

	t.Run("empty input leaves params Tags nil", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		var f dashboardFilters
		parseDashboardTags(url.Values{}, &p, &f)

		if p.Tags != nil {
			t.Errorf("expected Tags nil, got %v", p.Tags)
		}
	})
}

func TestParseBoundedInt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		query    url.Values
		key      string
		minimum  int
		fallback int
		want     int
	}{
		{"missing key returns fallback", url.Values{}, "limit", 1, 50, 50},
		{"valid above minimum", url.Values{"limit": {"25"}}, "limit", 1, 50, 25},
		{"unparseable returns fallback", url.Values{"limit": {"abc"}}, "limit", 1, 50, 50},
		{"below minimum returns fallback", url.Values{"limit": {"0"}}, "limit", 1, 50, 50},
		{"negative below minimum 0 returns fallback", url.Values{"offset": {"-1"}}, "offset", 0, 0, 0},
		{"zero is valid when minimum is 0", url.Values{"offset": {"0"}}, "offset", 0, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := parseBoundedInt(tc.query, tc.key, tc.minimum, tc.fallback)
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildDashboardEvents(t *testing.T) {
	t.Parallel()

	t.Run("empty events returns empty slice", func(t *testing.T) {
		t.Parallel()

		out := buildDashboardEvents(nil, nil)
		if len(out) != 0 {
			t.Errorf("expected empty slice, got %d entries", len(out))
		}
	})

	t.Run("events with annotations have merged state", func(t *testing.T) {
		t.Parallel()

		events := []model.ChangeEvent{
			{ID: "ev1"},
			{ID: "ev2"},
		}
		ann := map[string]*model.EventAnnotations{
			"ev1": {Starred: true, Alerted: false},
			"ev2": {Starred: false, Alerted: true},
		}

		out := buildDashboardEvents(events, ann)

		if len(out) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(out))
		}
		if out[0].ID != "ev1" || !out[0].Starred || out[0].Alerted {
			t.Errorf("ev1 merged incorrectly: %+v", out[0])
		}
		if out[1].ID != "ev2" || out[1].Starred || !out[1].Alerted {
			t.Errorf("ev2 merged incorrectly: %+v", out[1])
		}
	})

	t.Run("events missing from annotations map default to false", func(t *testing.T) {
		t.Parallel()

		events := []model.ChangeEvent{{ID: "orphan"}}
		out := buildDashboardEvents(events, map[string]*model.EventAnnotations{})

		if len(out) != 1 {
			t.Fatalf("len = %d, want 1", len(out))
		}
		if out[0].Starred || out[0].Alerted {
			t.Errorf("expected Starred/Alerted false for missing annotation, got %+v", out[0])
		}
	})

	// This is the branch the refactor explicitly added with the `ann != nil`
	// guard — the map reports "present" but the value is nil. The function
	// must not dereference nil and must fall back to the default false state.
	t.Run("nil annotation value is treated as no annotation", func(t *testing.T) {
		t.Parallel()

		events := []model.ChangeEvent{{ID: "nil-ann"}}
		ann := map[string]*model.EventAnnotations{"nil-ann": nil}

		out := buildDashboardEvents(events, ann)

		if len(out) != 1 {
			t.Fatalf("len = %d, want 1", len(out))
		}
		if out[0].Starred || out[0].Alerted {
			t.Errorf("expected false for nil annotation, got %+v", out[0])
		}
	})
}
