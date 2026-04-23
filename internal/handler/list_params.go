package handler

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// paramError carries a 400-level error code and message produced while parsing
// query parameters. Same-package callers read code and message directly to
// build the JSON error response; Error() lets it double as a standard error.
type paramError struct {
	code    string
	message string
}

func (e *paramError) Error() string { return e.message }

// parseListParams translates URL query values into a model.ListParams.
// A non-nil return indicates bad input ready to send as a 400 response;
// nil indicates a valid, populated params value.
func parseListParams(q url.Values) (model.ListParams, *paramError) {
	var p model.ListParams

	if err := parseRFC3339Param(q, "start_after", &p.StartAfter); err != nil {
		return p, err
	}
	if err := parseRFC3339Param(q, "start_before", &p.StartBefore); err != nil {
		return p, err
	}
	if err := parseAroundWindow(q, &p); err != nil {
		return p, err
	}

	p.UserName = q.Get("user")
	p.EventType = q.Get("type")
	p.TopLevel = q.Get("top_level") == "true"
	p.AlertedOnly = q.Get("alerted") == "true"

	if err := parseTagParams(q["tag"], &p.Tags); err != nil {
		return p, err
	}
	if err := parseIntParam(q, "limit", &p.Limit); err != nil {
		return p, err
	}
	if err := parseIntParam(q, "offset", &p.Offset); err != nil {
		return p, err
	}

	return p, nil
}

// parseRFC3339Param parses an optional RFC3339 timestamp query parameter.
// Writes to dest only when the key is present and parse succeeds.
func parseRFC3339Param(q url.Values, key string, dest **time.Time) *paramError {
	v := q.Get(key)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return &paramError{
			code:    "invalid_parameter",
			message: key + " must be RFC3339 format",
		}
	}
	*dest = &t
	return nil
}

// parseAroundWindow handles the paired "around" (RFC3339) and "window" (duration)
// query parameters. "window" is only consulted when "around" is present, and
// defaults to 30m when omitted in that case — matching the documented API.
func parseAroundWindow(q url.Values, p *model.ListParams) *paramError {
	v := q.Get("around")
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return &paramError{
			code:    "invalid_parameter",
			message: "around must be RFC3339 format",
		}
	}
	p.Around = &t

	windowStr := q.Get("window")
	if windowStr == "" {
		windowStr = "30m"
	}
	d, err := time.ParseDuration(windowStr)
	if err != nil {
		return &paramError{
			code:    "invalid_parameter",
			message: "window must be a valid duration (e.g., 30m, 1h)",
		}
	}
	p.Window = &d
	return nil
}

// parseTagParams converts repeated "tag=key:value" query values into a map.
// Each value must contain a colon separating a non-empty key from a value;
// otherwise the whole request is rejected with a 400.
func parseTagParams(tagValues []string, dest *map[string]string) *paramError {
	if len(tagValues) == 0 {
		return nil
	}
	tags := make(map[string]string, len(tagValues))
	for _, tv := range tagValues {
		k, v, ok := strings.Cut(tv, ":")
		if !ok || k == "" {
			return &paramError{
				code:    "invalid_parameter",
				message: "tag must be in key:value format",
			}
		}
		tags[k] = v
	}
	*dest = tags
	return nil
}

// parseIntParam parses an optional integer query parameter.
// Writes to dest only when the key is present and parse succeeds.
func parseIntParam(q url.Values, key string, dest *int) *paramError {
	v := q.Get(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return &paramError{
			code:    "invalid_parameter",
			message: key + " must be an integer",
		}
	}
	*dest = n
	return nil
}
