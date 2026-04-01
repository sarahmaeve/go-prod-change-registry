package model

import "time"

// Well-known event type constants.
const (
	EventTypeDeployment  = "deployment"
	EventTypeFeatureFlag = "feature-flag"
	EventTypeK8sChange   = "k8s-change"
)

// ChangeEvent represents a single production change recorded in the registry.
type ChangeEvent struct {
	ID              string            `json:"id"`
	UserName        string            `json:"user_name"`
	TimestampStart  time.Time         `json:"timestamp_start"`
	TimestampEnd    *time.Time        `json:"timestamp_end,omitempty"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Starred         bool              `json:"starred"`
	Alerted         bool              `json:"alerted"`
	Tags            map[string]string `json:"tags,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// ListParams holds the filtering and pagination parameters for listing change events.
type ListParams struct {
	StartAfter  *time.Time        `json:"start_after,omitempty"`
	StartBefore *time.Time        `json:"start_before,omitempty"`
	UserName    string            `json:"user_name,omitempty"`
	EventType   string            `json:"event_type,omitempty"`
	Alerted     *bool             `json:"alerted,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Limit       int               `json:"limit"`
	Offset      int               `json:"offset"`
}

// DefaultLimit is the default number of results returned by List.
const DefaultLimit = 50

// EffectiveLimit returns the Limit to use, defaulting to DefaultLimit when zero.
func (p ListParams) EffectiveLimit() int {
	if p.Limit <= 0 {
		return DefaultLimit
	}
	return p.Limit
}

// ListResult is the paginated result of a List query.
type ListResult struct {
	Events     []ChangeEvent `json:"events"`
	TotalCount int           `json:"total_count"`
	Limit      int           `json:"limit"`
	Offset     int           `json:"offset"`
}

// CreateChangeRequest is the payload for creating a new change event.
type CreateChangeRequest struct {
	UserName        string            `json:"user_name"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	TimestampStart  *time.Time        `json:"timestamp_start,omitempty"`
	TimestampEnd    *time.Time        `json:"timestamp_end,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// UpdateChangeRequest is the payload for partially updating a change event.
// All fields are pointers so that only provided fields are applied.
type UpdateChangeRequest struct {
	UserName        *string            `json:"user_name,omitempty"`
	EventType       *string            `json:"event_type,omitempty"`
	Description     *string            `json:"description,omitempty"`
	LongDescription *string            `json:"long_description,omitempty"`
	TimestampStart  *time.Time         `json:"timestamp_start,omitempty"`
	TimestampEnd    *time.Time         `json:"timestamp_end,omitempty"`
	Tags            *map[string]string `json:"tags,omitempty"`
	Starred         *bool              `json:"starred,omitempty"`
	Alerted         *bool              `json:"alerted,omitempty"`
}
