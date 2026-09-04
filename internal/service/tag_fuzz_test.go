package service //nolint:testpackage // fuzzing the unexported tag policy is intentional

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
)

func FuzzEventTagPolicy(f *testing.F) {
	for _, seed := range []struct {
		eventType, phase, changeID, deployID, team, severity, scope string
		present                                                     uint8
	}{
		{model.EventTypeMaintenance, "start", "change-1", "", "platform", "sev1", "site", 0b1111111},
		{" Maintenance ", " end ", "", " deploy-1 ", " payments ", " SEV2 ", " SYSTEM ", 0b1111111},
		{model.EventTypeDeployment, "pending", "change-1", "", "", "critical", "regional", 0b1100111},
		{model.EventTypeDeployment, "", "", "", "", "", "", 0},
	} {
		f.Add(seed.eventType, seed.phase, seed.changeID, seed.deployID, seed.team, seed.severity, seed.scope, seed.present)
	}

	f.Fuzz(func(t *testing.T, eventType, phase, changeID, deployID, team, severity, scope string, present uint8) {
		values := []struct {
			key   string
			value string
		}{
			{"phase", phase},
			{"change_id", changeID},
			{"deploy_id", deployID},
			{"team", team},
			{"severity", severity},
			{"scope", scope},
		}
		input := make(map[string]string)
		for i, value := range values {
			if present&(1<<i) != 0 {
				input[value.key] = value.value
			}
		}
		original := maps.Clone(input)

		normalized := normalizeEventTags(input)
		if !maps.Equal(input, original) {
			t.Fatalf("normalizeEventTags() mutated input: got %v, want %v", input, original)
		}
		if repeated := normalizeEventTags(input); !maps.Equal(normalized, repeated) {
			t.Fatalf("normalizeEventTags() is not deterministic: first %v, repeated %v", normalized, repeated)
		}
		if repeated := normalizeEventTags(normalized); !maps.Equal(normalized, repeated) {
			t.Fatalf("normalizeEventTags() is not idempotent: first %v, repeated %v", normalized, repeated)
		}

		canonicalType := eventType
		if strings.EqualFold(strings.TrimSpace(canonicalType), model.EventTypeMaintenance) {
			canonicalType = model.EventTypeMaintenance
		}
		err := validateEventTags(canonicalType, normalized)
		if err != nil {
			if !errors.Is(err, ErrInvalidTags) {
				t.Fatalf("validateEventTags() error = %v, want ErrInvalidTags", err)
			}
			return
		}

		assertAcceptedEventTags(t, canonicalType, normalized)
	})
}

func assertAcceptedEventTags(t *testing.T, eventType string, tags map[string]string) {
	t.Helper()

	for _, key := range []string{"phase", "change_id", "deploy_id", "team", "severity", "scope"} {
		if value, ok := tags[key]; ok && value != strings.TrimSpace(value) {
			t.Fatalf("accepted tag %q has untrimmed value %q", key, value)
		}
	}
	phase, hasPhase := tags["phase"]
	_, hasChangeID := tags["change_id"]
	_, hasDeployID := tags["deploy_id"]
	hasIdentifier := tags["change_id"] != "" || tags["deploy_id"] != ""
	hasIdentifierTag := hasChangeID || hasDeployID
	validPhase := phase == "start" || phase == "end"
	if eventType == model.EventTypeMaintenance && (!hasPhase || !validPhase || !hasIdentifier) {
		t.Fatalf("accepted maintenance tags do not define a lifecycle: %v", tags)
	}
	if hasPhase && (!validPhase || !hasIdentifier) {
		t.Fatalf("accepted phase tag is incomplete or invalid: %v", tags)
	}
	if !hasPhase && hasIdentifierTag {
		t.Fatalf("accepted identifier tag has no phase: %v", tags)
	}
	if severity, ok := tags["severity"]; ok && severity != "sev0" && severity != "sev1" && severity != "sev2" && severity != "sev3" {
		t.Fatalf("accepted invalid severity %q", severity)
	}
	if scope, ok := tags["scope"]; ok && scope != "service" && scope != "system" && scope != "site" {
		t.Fatalf("accepted invalid scope %q", scope)
	}
}
