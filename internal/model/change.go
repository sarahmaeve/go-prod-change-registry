package model

import "time"

// UserIdentity is the authenticated actor attached to an immutable event.
// Provider and Subject are empty for API-supplied user names.
type UserIdentity struct {
	Name     string
	Provider string
	Subject  string
}

// Well-known event type constants.
const (
	EventTypeDeployment  = "deployment"
	EventTypeFeatureFlag = "feature-flag"
	EventTypeK8sChange   = "k8s-change"
	EventTypeMaintenance = "maintenance"

	// Meta-event types for annotations.
	EventTypeStar       = "star"
	EventTypeUnstar     = "unstar"
	EventTypeAlert      = "alert"
	EventTypeClearAlert = "clear-alert"
	EventTypeLink       = "link"
)

// Derived logical-operation states.
const (
	OperationStateOpen   = "open"
	OperationStateClosed = "closed"
)

// ChangeEvent represents a single production change or meta-event recorded in the registry.
// Events are immutable once created. Status changes (star, alert) are modeled as
// meta-events with a ParentID referencing the original event.
type ChangeEvent struct {
	ID              string            `json:"id"`
	ExternalID      string            `json:"external_id,omitempty"`
	ParentID        string            `json:"parent_id,omitempty"`
	UserName        string            `json:"user_name"`
	UserProvider    string            `json:"user_provider,omitempty"`
	UserSubject     string            `json:"user_subject,omitempty"`
	Timestamp       time.Time         `json:"timestamp"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description"`
	Links           []EventLink       `json:"links,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
}

// EventLink is an ordered external reference attached to an event.
type EventLink struct {
	Label string `json:"label,omitempty"`
	URL   string `json:"url"`
}

// IsMetaEvent returns true if this event is an annotation on another event.
func (e ChangeEvent) IsMetaEvent() bool {
	return e.ParentID != ""
}

// ListParams holds the filtering and pagination parameters for listing change events.
type ListParams struct {
	ParentID    string            `json:"parent_id,omitempty"`
	StartAfter  *time.Time        `json:"start_after,omitempty"`
	StartBefore *time.Time        `json:"start_before,omitempty"`
	Around      *time.Time        `json:"around,omitempty"`
	Window      *time.Duration    `json:"window,omitempty"`
	UserName    string            `json:"user_name,omitempty"`
	EventType   string            `json:"event_type,omitempty"`
	TopLevel    bool              `json:"top_level,omitempty"`
	AlertedOnly bool              `json:"alerted_only,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Limit       int               `json:"limit"`
	Offset      int               `json:"offset"`
}

// CurrentParams holds filtering and pagination parameters for active logical operations.
type CurrentParams struct {
	ForTeam          string   `json:"for_team,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	Severities       []string `json:"severities,omitempty"`
	EventType        string   `json:"event_type,omitempty"`
	CorrelationKey   string   `json:"-"`
	CorrelationValue string   `json:"-"`
	Limit            int      `json:"limit"`
	Offset           int      `json:"offset"`
}

// DefaultLimit is the default number of results returned by the API.
const DefaultLimit = 50

// DashboardLimit is the default number of results shown in the web dashboard.
const DashboardLimit = 40

// MaxLimit is the maximum number of results allowed per query.
const MaxLimit = 200

// EffectiveLimit returns the Limit to use, clamped to [1, 200] with a default of 50.
func (p ListParams) EffectiveLimit() int {
	return effectiveLimit(p.Limit)
}

// EffectiveLimit returns the Limit to use, clamped to [1, 200] with a default of 50.
func (p CurrentParams) EffectiveLimit() int {
	return effectiveLimit(p.Limit)
}

func effectiveLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultLimit
	case limit > MaxLimit:
		return MaxLimit
	default:
		return limit
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
	ExternalID      string            `json:"external_id,omitempty"`
	UserName        string            `json:"user_name"`
	UserProvider    string            `json:"-"`
	UserSubject     string            `json:"-"`
	Timestamp       *time.Time        `json:"timestamp,omitempty"`
	EventType       string            `json:"event_type"`
	Description     string            `json:"description"`
	LongDescription string            `json:"long_description,omitempty"`
	Links           []EventLink       `json:"links,omitempty"`
	Tags            map[string]string `json:"tags,omitempty"`
}

// AddLinksRequest appends external references to an existing event.
type AddLinksRequest struct {
	UserName string      `json:"user_name"`
	Links    []EventLink `json:"links"`
}

// CloseOperationRequest appends an end event for an active operation.
type CloseOperationRequest struct {
	UserName    string `json:"user_name"`
	Description string `json:"description,omitempty"`
}

// EventAnnotations holds the derived annotation state for an event.
type EventAnnotations struct {
	Starred bool `json:"starred"`
	Alerted bool `json:"alerted"`
}
