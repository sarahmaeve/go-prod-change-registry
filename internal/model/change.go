package model

import "time"

// Well-known event type constants.
const (
	EventTypeDeployment  = "deployment"
	EventTypeFeatureFlag = "feature-flag"
	EventTypeK8sChange   = "k8s-change"

	// Meta-event types for annotations.
	EventTypeStar      = "star"
	EventTypeUnstar    = "unstar"
	EventTypeAlert     = "alert"
	EventTypeClearAlert = "clear-alert"
)

// ChangeEvent represents a single production change or meta-event recorded in the registry.
// Events are immutable once created. Status changes (star, alert) are modeled as
// meta-events with a ParentID referencing the original event.
type ChangeEvent struct {
	ID              string            `json:"id"`
	ParentID        string            `json:"parent_id,omitempty"`
	UserName        string            `json:"user_name"`
	Timestamp       time.Time         `json:"timestamp"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Tags            map[string]string `json:"tags,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
}

// IsMetaEvent returns true if this event is an annotation on another event.
func (e ChangeEvent) IsMetaEvent() bool {
	return e.ParentID != ""
}

// ListParams holds the filtering and pagination parameters for listing change events.
type ListParams struct {
	StartAfter  *time.Time        `json:"start_after,omitempty"`
	StartBefore *time.Time        `json:"start_before,omitempty"`
	Around      *time.Time        `json:"around,omitempty"`
	Window      *time.Duration    `json:"window,omitempty"`
	UserName    string            `json:"user_name,omitempty"`
	EventType   string            `json:"event_type,omitempty"`
	TopLevel    bool              `json:"top_level,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Limit       int               `json:"limit"`
	Offset      int               `json:"offset"`
}

// DefaultLimit is the default number of results returned by List.
const DefaultLimit = 50

// EffectiveLimit returns the Limit to use, clamped to [1, 200] with a default of 50.
func (p ListParams) EffectiveLimit() int {
	switch {
	case p.Limit <= 0:
		return DefaultLimit
	case p.Limit > 200:
		return 200
	default:
		return p.Limit
	}
}

// ListResult is the paginated result of a List query.
type ListResult struct {
	Events     []ChangeEvent `json:"events"`
	TotalCount int           `json:"total_count"`
	Limit      int           `json:"limit"`
	Offset     int           `json:"offset"`
}

// CreateChangeRequest is the API request body for creating a new change event.
type CreateChangeRequest struct {
	ParentID        string            `json:"parent_id,omitempty"`
	UserName        string            `json:"user_name"`
	Timestamp       *time.Time        `json:"timestamp,omitempty"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// EventAnnotations holds the derived annotation state for an event.
type EventAnnotations struct {
	Starred bool `json:"starred"`
	Alerted bool `json:"alerted"`
}
