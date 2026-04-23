package handler

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// White-box tests for the query-parameter parser introduced alongside the
// ListEvents refactor. These exercise each helper directly, without going
// through an HTTP server, so failures point at the parsing logic and not
// at handler wiring.

func TestParseListParams(t *testing.T) {
	t.Parallel()

	t.Run("empty query returns zero-value params", func(t *testing.T) {
		t.Parallel()

		p, perr := parseListParams(url.Values{})
		if perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if p.StartAfter != nil || p.StartBefore != nil || p.Around != nil || p.Window != nil {
			t.Errorf("expected all time/window pointers to be nil, got %+v", p)
		}
		if p.UserName != "" || p.EventType != "" {
			t.Errorf("expected empty filter strings, got user=%q type=%q", p.UserName, p.EventType)
		}
		if p.TopLevel || p.AlertedOnly {
			t.Errorf("expected TopLevel/AlertedOnly to default to false")
		}
		if len(p.Tags) != 0 {
			t.Errorf("expected Tags to be empty, got %v", p.Tags)
		}
		if p.Limit != 0 || p.Offset != 0 {
			t.Errorf("expected zero limit/offset, got limit=%d offset=%d", p.Limit, p.Offset)
		}
	})

	t.Run("all supported fields populate params", func(t *testing.T) {
		t.Parallel()

		q := url.Values{}
		q.Set("start_after", "2026-04-20T00:00:00Z")
		q.Set("start_before", "2026-04-25T00:00:00Z")
		q.Set("user", "alice")
		q.Set("type", "deploy")
		q.Set("top_level", "true")
		q.Set("alerted", "true")
		q.Set("limit", "25")
		q.Set("offset", "10")
		q.Add("tag", "env:prod")
		q.Add("tag", "region:us-east")

		p, perr := parseListParams(q)
		if perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if p.StartAfter == nil || !p.StartAfter.Equal(time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("StartAfter = %v, want 2026-04-20T00:00:00Z", p.StartAfter)
		}
		if p.StartBefore == nil || !p.StartBefore.Equal(time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("StartBefore = %v, want 2026-04-25T00:00:00Z", p.StartBefore)
		}
		if p.UserName != "alice" || p.EventType != "deploy" {
			t.Errorf("UserName/EventType = %q/%q, want alice/deploy", p.UserName, p.EventType)
		}
		if !p.TopLevel || !p.AlertedOnly {
			t.Errorf("TopLevel/AlertedOnly = %v/%v, want true/true", p.TopLevel, p.AlertedOnly)
		}
		if p.Limit != 25 || p.Offset != 10 {
			t.Errorf("Limit/Offset = %d/%d, want 25/10", p.Limit, p.Offset)
		}
		if p.Tags["env"] != "prod" || p.Tags["region"] != "us-east" {
			t.Errorf("Tags = %v, want env=prod region=us-east", p.Tags)
		}
	})

	t.Run("top_level accepts only literal 'true'", func(t *testing.T) {
		t.Parallel()

		cases := []struct {
			raw  string
			want bool
		}{
			{"true", true},
			{"True", false},
			{"TRUE", false},
			{"1", false},
			{"yes", false},
			{"", false},
		}
		for _, tc := range cases {
			q := url.Values{}
			if tc.raw != "" {
				q.Set("top_level", tc.raw)
			}
			p, perr := parseListParams(q)
			if perr != nil {
				t.Fatalf("top_level=%q: unexpected error: %v", tc.raw, perr)
			}
			if p.TopLevel != tc.want {
				t.Errorf("top_level=%q: TopLevel = %v, want %v", tc.raw, p.TopLevel, tc.want)
			}
		}
	})

	errCases := []struct {
		name         string
		query        url.Values
		wantContains string
	}{
		{
			name:         "invalid start_after",
			query:        url.Values{"start_after": {"not-a-date"}},
			wantContains: "start_after",
		},
		{
			name:         "invalid start_before",
			query:        url.Values{"start_before": {"not-a-date"}},
			wantContains: "start_before",
		},
		{
			name:         "invalid around",
			query:        url.Values{"around": {"not-a-date"}},
			wantContains: "around",
		},
		{
			name: "invalid window with valid around",
			query: url.Values{
				"around": {"2026-04-23T00:00:00Z"},
				"window": {"not-a-duration"},
			},
			wantContains: "window",
		},
		{
			name:         "malformed tag without colon",
			query:        url.Values{"tag": {"nocolon"}},
			wantContains: "tag",
		},
		{
			name:         "tag with empty key",
			query:        url.Values{"tag": {":value"}},
			wantContains: "tag",
		},
		{
			name:         "non-numeric limit",
			query:        url.Values{"limit": {"abc"}},
			wantContains: "limit",
		},
		{
			name:         "non-numeric offset",
			query:        url.Values{"offset": {"abc"}},
			wantContains: "offset",
		},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, perr := parseListParams(tc.query)
			if perr == nil {
				t.Fatalf("expected error, got nil")
			}
			if perr.code != "invalid_parameter" {
				t.Errorf("code = %q, want invalid_parameter", perr.code)
			}
			if !strings.Contains(perr.message, tc.wantContains) {
				t.Errorf("message = %q, expected to contain %q", perr.message, tc.wantContains)
			}
		})
	}
}

// TestParseAroundWindow targets the three branches the original refactor
// review flagged as uncovered: around alone defaults window, invalid
// window returns 400, and window without around is a complete no-op.
func TestParseAroundWindow(t *testing.T) {
	t.Parallel()

	t.Run("around alone defaults window to 30m", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		q := url.Values{"around": {"2026-04-23T00:00:00Z"}}
		if perr := parseAroundWindow(q, &p); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if p.Around == nil {
			t.Fatal("Around is nil")
		}
		if p.Window == nil || *p.Window != 30*time.Minute {
			t.Errorf("Window = %v, want 30m default", p.Window)
		}
	})

	t.Run("window without around is a no-op", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		q := url.Values{"window": {"1h"}}
		if perr := parseAroundWindow(q, &p); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if p.Around != nil || p.Window != nil {
			t.Errorf("expected Around and Window nil, got %+v / %+v", p.Around, p.Window)
		}
	})

	t.Run("custom window duration is honored when around is set", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		q := url.Values{
			"around": {"2026-04-23T00:00:00Z"},
			"window": {"2h"},
		}
		if perr := parseAroundWindow(q, &p); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if p.Window == nil || *p.Window != 2*time.Hour {
			t.Errorf("Window = %v, want 2h", p.Window)
		}
	})

	t.Run("invalid window returns 400-shaped error", func(t *testing.T) {
		t.Parallel()

		var p model.ListParams
		q := url.Values{
			"around": {"2026-04-23T00:00:00Z"},
			"window": {"forever"},
		}
		perr := parseAroundWindow(q, &p)
		if perr == nil {
			t.Fatal("expected error, got nil")
		}
		if perr.code != "invalid_parameter" || !strings.Contains(perr.message, "window") {
			t.Errorf("got code=%q message=%q, want invalid_parameter/window", perr.code, perr.message)
		}
	})
}

func TestParseTagParams(t *testing.T) {
	t.Parallel()

	t.Run("empty input is a no-op", func(t *testing.T) {
		t.Parallel()

		var tags map[string]string
		if perr := parseTagParams(nil, &tags); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if tags != nil {
			t.Errorf("expected tags unchanged (nil), got %v", tags)
		}
	})

	t.Run("multiple valid tags populate map", func(t *testing.T) {
		t.Parallel()

		var tags map[string]string
		if perr := parseTagParams([]string{"env:prod", "tier:1"}, &tags); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if tags["env"] != "prod" || tags["tier"] != "1" {
			t.Errorf("tags = %v, want env=prod tier=1", tags)
		}
	})

	t.Run("tag value may contain additional colons", func(t *testing.T) {
		t.Parallel()

		var tags map[string]string
		if perr := parseTagParams([]string{"image:alpine:3.19"}, &tags); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if tags["image"] != "alpine:3.19" {
			t.Errorf("tags[image] = %q, want alpine:3.19", tags["image"])
		}
	})
}

func TestParseIntParam(t *testing.T) {
	t.Parallel()

	t.Run("missing key leaves dest untouched", func(t *testing.T) {
		t.Parallel()

		dest := 99
		if perr := parseIntParam(url.Values{}, "limit", &dest); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if dest != 99 {
			t.Errorf("dest = %d, want unchanged 99", dest)
		}
	})

	t.Run("valid integer writes to dest", func(t *testing.T) {
		t.Parallel()

		dest := 0
		if perr := parseIntParam(url.Values{"limit": {"42"}}, "limit", &dest); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if dest != 42 {
			t.Errorf("dest = %d, want 42", dest)
		}
	})

	t.Run("negative integer is accepted", func(t *testing.T) {
		t.Parallel()

		dest := 0
		if perr := parseIntParam(url.Values{"offset": {"-5"}}, "offset", &dest); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if dest != -5 {
			t.Errorf("dest = %d, want -5", dest)
		}
	})
}

func TestParseRFC3339Param(t *testing.T) {
	t.Parallel()

	t.Run("missing key leaves dest nil", func(t *testing.T) {
		t.Parallel()

		var dest *time.Time
		if perr := parseRFC3339Param(url.Values{}, "start_after", &dest); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if dest != nil {
			t.Errorf("dest = %v, want nil", dest)
		}
	})

	t.Run("valid RFC3339 writes pointer", func(t *testing.T) {
		t.Parallel()

		var dest *time.Time
		q := url.Values{"start_after": {"2026-04-23T12:00:00Z"}}
		if perr := parseRFC3339Param(q, "start_after", &dest); perr != nil {
			t.Fatalf("unexpected error: %v", perr)
		}
		if dest == nil || !dest.Equal(time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)) {
			t.Errorf("dest = %v, want 2026-04-23T12:00:00Z", dest)
		}
	})

	t.Run("error message includes the parameter name", func(t *testing.T) {
		t.Parallel()

		var dest *time.Time
		perr := parseRFC3339Param(url.Values{"start_before": {"garbage"}}, "start_before", &dest)
		if perr == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(perr.message, "start_before") {
			t.Errorf("message = %q, expected to mention start_before", perr.message)
		}
	})
}
