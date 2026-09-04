package handler //nolint:testpackage // white-box tests cover the unexported query parser

import (
	"net/url"
	"testing"
)

func TestParseCurrentParams(t *testing.T) {
	t.Parallel()

	t.Run("parses filters and repeated values", func(t *testing.T) {
		t.Parallel()

		params, err := parseCurrentParams(url.Values{
			"for_team": {"payments"},
			"scope":    {"service", "site"},
			"severity": {"sev0", "sev1"},
			"type":     {"deployment"},
			"limit":    {"25"},
			"offset":   {"50"},
		})
		if err != nil {
			t.Fatalf("parseCurrentParams() error = %v", err)
		}
		if params.ForTeam != "payments" || params.EventType != "deployment" {
			t.Errorf("scalar params = %+v", params)
		}
		if len(params.Scopes) != 2 || params.Scopes[0] != "service" || params.Scopes[1] != "site" {
			t.Errorf("Scopes = %v, want [service site]", params.Scopes)
		}
		if len(params.Severities) != 2 || params.Severities[0] != "sev0" || params.Severities[1] != "sev1" {
			t.Errorf("Severities = %v, want [sev0 sev1]", params.Severities)
		}
		if params.Limit != 25 || params.Offset != 50 {
			t.Errorf("Limit/Offset = %d/%d, want 25/50", params.Limit, params.Offset)
		}
	})

	t.Run("canonicalizes well-known filter values", func(t *testing.T) {
		t.Parallel()

		params, err := parseCurrentParams(url.Values{
			"for_team": {" platform "},
			"scope":    {" SITE ", ""},
			"severity": {" SEV1 "},
			"type":     {" maintenance "},
		})
		if err != nil {
			t.Fatalf("parseCurrentParams() error = %v", err)
		}
		if params.ForTeam != "platform" || params.EventType != "maintenance" ||
			len(params.Scopes) != 1 || params.Scopes[0] != "site" ||
			len(params.Severities) != 1 || params.Severities[0] != "sev1" {
			t.Fatalf("parseCurrentParams() = %+v", params)
		}
	})

	for _, key := range []string{"limit", "offset"} {
		t.Run("rejects malformed "+key, func(t *testing.T) {
			t.Parallel()

			_, err := parseCurrentParams(url.Values{key: {"not-an-integer"}})
			if err == nil {
				t.Fatalf("parseCurrentParams(%s) error = nil", key)
			}
			if err.code != "invalid_parameter" || err.message != key+" must be an integer" {
				t.Errorf("error = %#v", err)
			}
		})
	}
}
