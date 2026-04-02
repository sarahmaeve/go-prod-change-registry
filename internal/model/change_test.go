package model_test

import (
	"testing"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

func TestEffectiveLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "zero defaults to 50", limit: 0, want: 50},
		{name: "negative defaults to 50", limit: -1, want: 50},
		{name: "minimum boundary 1", limit: 1, want: 1},
		{name: "normal value 50", limit: 50, want: 50},
		{name: "max boundary 200", limit: 200, want: 200},
		{name: "above max clamped to 200", limit: 201, want: 200},
		{name: "far above max clamped to 200", limit: 999, want: 200},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			p := model.ListParams{Limit: tc.limit}
			got := p.EffectiveLimit()
			if got != tc.want {
				t.Errorf("ListParams{Limit: %d}.EffectiveLimit() = %d, want %d",
					tc.limit, got, tc.want)
			}
		})
	}
}

func TestIsMetaEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		parentID string
		want     bool
	}{
		{name: "empty ParentID is not a meta event", parentID: "", want: false},
		{name: "non-empty ParentID is a meta event", parentID: "evt-123", want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := model.ChangeEvent{ParentID: tc.parentID}
			got := e.IsMetaEvent()
			if got != tc.want {
				t.Errorf("ChangeEvent{ParentID: %q}.IsMetaEvent() = %v, want %v",
					tc.parentID, got, tc.want)
			}
		})
	}
}
