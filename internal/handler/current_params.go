package handler

import (
	"net/url"
	"strings"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

func parseCurrentParams(q url.Values) (model.CurrentParams, *paramError) {
	params := model.CurrentParams{
		ForTeam:    strings.TrimSpace(q.Get("for_team")),
		Scopes:     normalizeCurrentValues(q["scope"]),
		Severities: normalizeCurrentValues(q["severity"]),
		EventType:  strings.TrimSpace(q.Get("type")),
	}
	if err := parseIntParam(q, "limit", &params.Limit); err != nil {
		return params, err
	}
	if err := parseIntParam(q, "offset", &params.Offset); err != nil {
		return params, err
	}
	return params, nil
}

func normalizeCurrentValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
