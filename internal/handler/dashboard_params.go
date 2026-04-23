package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// customRangeLayout is the datetime-local form input format used by the
// dashboard's "custom" range fields.
const customRangeLayout = "2006-01-02T15:04"

// parseDashboardRequest translates query parameters into a ListParams for
// the service layer and a dashboardFilters for re-populating the form.
//
// Unlike the JSON API, the dashboard tolerates malformed values silently:
// bad inputs fall back to sensible defaults so the page still renders. The
// original form value (even when unparseable) is preserved in filters so
// the user sees exactly what they typed.
func parseDashboardRequest(r *http.Request) (model.ListParams, dashboardFilters) {
	q := r.URL.Query()

	params := model.ListParams{TopLevel: true}
	filters := dashboardFilters{}

	parseDashboardRange(q, &params, &filters)

	if q.Get("alerted") == "true" {
		filters.Alerted = true
		params.AlertedOnly = true
	}

	if v := q.Get("type"); v != "" {
		filters.EventType = v
		params.EventType = v
	}

	if v := q.Get("user"); v != "" {
		filters.UserName = v
		params.UserName = v
	}

	parseDashboardTags(q, &params, &filters)

	params.Limit = parseBoundedInt(q, "limit", 1, model.DashboardLimit)
	params.Offset = parseBoundedInt(q, "offset", 0, 0)

	return params, filters
}

// parseDashboardRange handles the "range" query parameter: either a
// quick-select preset ("5m"/"30m"/"1h"/"24h") or "custom" with a pair of
// start_after/start_before datetime-local values. Defaults to the last
// 24 hours when the range is unset or unrecognized.
func parseDashboardRange(q url.Values, params *model.ListParams, filters *dashboardFilters) {
	rangeVal := q.Get("range")
	if rangeVal == "" {
		rangeVal = "24h"
	}
	filters.Range = rangeVal

	if d, ok := quickRanges[rangeVal]; ok {
		startAfter := time.Now().UTC().Add(-d)
		params.StartAfter = &startAfter
		return
	}
	if rangeVal != "custom" {
		return
	}

	// The raw string is always echoed back to the form, even if unparseable,
	// so the user can correct their input rather than lose it on submit.
	if v := q.Get("start_after"); v != "" {
		filters.StartAfter = v
		if t, err := time.Parse(customRangeLayout, v); err == nil {
			params.StartAfter = &t
		}
	}
	if v := q.Get("start_before"); v != "" {
		filters.StartBefore = v
		if t, err := time.Parse(customRangeLayout, v); err == nil {
			params.StartBefore = &t
		}
	}
}

// parseDashboardTags parses repeated "tag=key:value" query values into a
// map on params and a display slice on filters. Malformed entries are
// silently skipped — the dashboard is user-facing and must render even
// when an earlier link passed garbage.
func parseDashboardTags(q url.Values, params *model.ListParams, filters *dashboardFilters) {
	tagValues := q["tag"]
	if len(tagValues) == 0 {
		return
	}

	tags := make(map[string]string, len(tagValues))
	for _, tv := range tagValues {
		k, v, ok := strings.Cut(tv, ":")
		if !ok || k == "" {
			continue
		}
		tags[k] = v
		filters.Tags = append(filters.Tags, tv)
	}
	if len(tags) > 0 {
		params.Tags = tags
	}
}

// parseBoundedInt reads an integer query parameter and clamps it against
// a minimum. Missing, unparseable, or below-minimum values all return
// fallback — suitable for user-facing forms where we prefer a sensible
// default over a 400.
func parseBoundedInt(q url.Values, key string, minimum, fallback int) int {
	v := q.Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minimum {
		return fallback
	}
	return n
}
