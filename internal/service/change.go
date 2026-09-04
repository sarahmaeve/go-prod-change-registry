package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/sarahmaeve/go-prod-change-registry/internal/model"
	"github.com/sarahmaeve/go-prod-change-registry/internal/store"
)

var (
	ErrUserNameRequired     = errors.New("user_name is required")
	ErrEventTypeRequired    = errors.New("event_type is required")
	ErrInvalidTags          = errors.New("invalid event tags")
	ErrInvalidLink          = errors.New("links must have safe labels and absolute http or https URLs without credentials")
	ErrLinksRequired        = errors.New("at least one link is required")
	ErrEventNotFound        = errors.New("event not found")
	ErrParentNotFound       = errors.New("parent event not found")
	ErrOperationNotClosable = errors.New("event is not a correlated operation start")
	ErrOperationClosed      = errors.New("operation is already closed")
)

type ChangeService struct {
	store store.ChangeStore
}

func NewChangeService(store store.ChangeStore) *ChangeService {
	return &ChangeService{store: store}
}

func (s *ChangeService) Create(ctx context.Context, req *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	return s.create(ctx, req, true)
}

func (s *ChangeService) create(ctx context.Context, req *model.CreateChangeRequest, enforceTagPolicy bool) (*model.ChangeEvent, error) {
	// Preserve external_id idempotency across validation changes: retries of
	// records accepted by an older release still return the original event.
	if req.ExternalID != "" {
		existing, err := s.store.GetByExternalID(ctx, req.ExternalID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, store.ErrDuplicate
		}
	}
	if req.UserName == "" {
		return nil, ErrUserNameRequired
	}
	eventType := req.EventType
	if eventType == "" {
		return nil, ErrEventTypeRequired
	}
	tags := maps.Clone(req.Tags)
	if tags == nil {
		tags = make(map[string]string)
	}
	if enforceTagPolicy {
		if strings.EqualFold(strings.TrimSpace(eventType), model.EventTypeMaintenance) {
			eventType = model.EventTypeMaintenance
		}
		tags = normalizeEventTags(tags)
		if err := validateEventTags(eventType, tags); err != nil {
			return nil, err
		}
	}
	if err := validateLinks(req.Links); err != nil {
		return nil, err
	}

	// If this is a meta-event, verify the parent exists.
	if req.ParentID != "" {
		parent, err := s.store.GetByID(ctx, req.ParentID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, ErrParentNotFound
		}
	}

	now := time.Now().UTC()
	ts := now
	if req.Timestamp != nil {
		ts = req.Timestamp.UTC()
	}

	event := &model.ChangeEvent{
		ID:              uuid.Must(uuid.NewV7()).String(),
		ExternalID:      req.ExternalID,
		ParentID:        req.ParentID,
		UserName:        req.UserName,
		UserProvider:    req.UserProvider,
		UserSubject:     req.UserSubject,
		Timestamp:       ts,
		EventType:       eventType,
		Description:     req.Description,
		LongDescription: req.LongDescription,
		Links:           slices.Clone(req.Links),
		Tags:            tags,
		CreatedAt:       now,
	}

	created, err := s.store.Create(ctx, event)
	if errors.Is(err, store.ErrDuplicate) {
		return created, store.ErrDuplicate
	}
	return created, err
}

type invalidTagsError struct {
	message string
}

func (e invalidTagsError) Error() string { return e.message }
func (e invalidTagsError) Unwrap() error { return ErrInvalidTags }

func invalidTags(message string) error {
	return invalidTagsError{message: message}
}

func normalizeEventTags(input map[string]string) map[string]string {
	tags := maps.Clone(input)
	// The browser trims every tag value. Apply the same rule to the well-known
	// API tags whose exact values feed Current and its dashboard filters.
	for _, key := range []string{"phase", "change_id", "deploy_id", "team", "severity", "scope"} {
		if value, ok := tags[key]; ok {
			tags[key] = strings.TrimSpace(value)
		}
	}
	// Current queries accept legacy mixed-case severities, but canonical new
	// writes keep badge classes and exact history tag filters predictable.
	if severity, ok := tags["severity"]; ok {
		tags["severity"] = strings.ToLower(severity)
	}
	if scope, ok := tags["scope"]; ok {
		tags["scope"] = strings.ToLower(scope)
	}
	return tags
}

// validateEventTags protects the well-known tags that feed Current and its
// dashboard presets. Other tags remain intentionally free-form.
func validateEventTags(eventType string, tags map[string]string) error {
	phase, hasPhase := tags["phase"]
	changeID, hasChangeID := tags["change_id"]
	deployID, hasDeployID := tags["deploy_id"]
	hasIdentifier := changeID != "" || deployID != ""
	hasIdentifierTag := hasChangeID || hasDeployID
	validPhase := phase == "start" || phase == "end"

	if eventType == model.EventTypeMaintenance && (!hasPhase || !validPhase || !hasIdentifier) {
		return invalidTags("maintenance events require phase=start or phase=end and a non-empty change_id or deploy_id")
	}
	if hasPhase && !validPhase {
		return invalidTags("phase must be exactly start or end")
	}
	if hasPhase && !hasIdentifier {
		return invalidTags("phase requires a non-empty change_id or deploy_id")
	}
	if !hasPhase && hasIdentifierTag {
		return invalidTags("change_id and deploy_id require phase=start or phase=end")
	}

	if severity, ok := tags["severity"]; ok {
		switch strings.ToLower(severity) {
		case "sev0", "sev1", "sev2", "sev3":
		default:
			return invalidTags("severity must be sev0, sev1, sev2, or sev3")
		}
	}
	if scope, ok := tags["scope"]; ok {
		switch strings.ToLower(scope) {
		case "service", "system", "site":
		default:
			return invalidTags("scope must be service, system, or site")
		}
	}
	return nil
}

const (
	maxLinkLabelBytes = 256
	maxLinkURLBytes   = 2048
)

func validateLinks(links []model.EventLink) error {
	for _, link := range links {
		if len(link.Label) > maxLinkLabelBytes || strings.IndexFunc(link.Label, isUnsafeLinkRune) >= 0 {
			return ErrInvalidLink
		}
		if link.URL == "" || len(link.URL) > maxLinkURLBytes || link.URL != strings.TrimSpace(link.URL) || strings.Contains(link.URL, "\\") || strings.IndexFunc(link.URL, unicode.IsSpace) >= 0 {
			return ErrInvalidLink
		}
		parsed, err := url.ParseRequestURI(link.URL)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return ErrInvalidLink
		}
		unescaped, err := url.PathUnescape(link.URL)
		if err != nil || strings.Contains(unescaped, "\\") || strings.IndexFunc(unescaped, isUnsafeLinkRune) >= 0 {
			return ErrInvalidLink
		}
	}
	return nil
}

func isUnsafeLinkRune(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func (s *ChangeService) GetByID(ctx context.Context, id string) (*model.ChangeEvent, error) {
	event, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if event == nil {
		return nil, ErrEventNotFound
	}
	return event, nil
}

func (s *ChangeService) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	params.Limit = params.EffectiveLimit()
	return s.store.List(ctx, params)
}

// ListCurrent returns active logical operations derived from immutable phase events.
func (s *ChangeService) ListCurrent(ctx context.Context, params model.CurrentParams) (*model.ListResult, error) {
	params.Limit = params.EffectiveLimit()
	return s.store.ListCurrent(ctx, params)
}

func (s *ChangeService) GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error) {
	return s.store.GetAnnotations(ctx, eventID)
}

func (s *ChangeService) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error) {
	return s.store.GetAnnotationsBatch(ctx, eventIDs)
}

// AddLinks appends an immutable link annotation to an existing event.
func (s *ChangeService) AddLinks(ctx context.Context, eventID, userName string, links []model.EventLink) (*model.ChangeEvent, error) {
	return s.AddLinksAs(ctx, eventID, model.UserIdentity{Name: userName}, links)
}

// AddLinksAs appends links attributed to an authenticated identity.
func (s *ChangeService) AddLinksAs(ctx context.Context, eventID string, user model.UserIdentity, links []model.EventLink) (*model.ChangeEvent, error) {
	if len(links) == 0 {
		return nil, ErrLinksRequired
	}
	return s.Create(ctx, &model.CreateChangeRequest{
		ParentID:     eventID,
		UserName:     user.Name,
		UserProvider: user.Provider,
		UserSubject:  user.Subject,
		EventType:    model.EventTypeLink,
		Description:  "added external links",
		Links:        links,
	})
}

// GetActivity returns all child annotations for an event in chronological order.
func (s *ChangeService) GetActivity(ctx context.Context, eventID string) ([]model.ChangeEvent, error) {
	parent, err := s.GetByID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	activity, err := s.listAll(ctx, model.ListParams{ParentID: eventID})
	if err != nil {
		return nil, err
	}
	if key, value, ok := operationIdentity(parent); ok {
		closures, err := s.listAll(ctx, model.ListParams{
			EventType: parent.EventType,
			TopLevel:  true,
			Tags:      map[string]string{key: value, "phase": "end"},
		})
		if err != nil {
			return nil, err
		}
		activity = append(activity, closures...)
	}
	slices.SortFunc(activity, func(a, b model.ChangeEvent) int {
		if order := a.Timestamp.Compare(b.Timestamp); order != 0 {
			return order
		}
		return strings.Compare(a.ID, b.ID)
	})
	return activity, nil
}

func (s *ChangeService) listAll(ctx context.Context, params model.ListParams) ([]model.ChangeEvent, error) {
	params.Limit = model.MaxLimit
	events := make([]model.ChangeEvent, 0)
	for {
		result, err := s.List(ctx, params)
		if err != nil {
			return nil, err
		}
		events = append(events, result.Events...)
		if len(result.Events) == 0 || params.Offset+len(result.Events) >= result.TotalCount {
			return events, nil
		}
		params.Offset += len(result.Events)
	}
}

// OperationState returns open or closed for a correlated start event. Events
// that do not represent a closable operation have no lifecycle state.
func (s *ChangeService) OperationState(ctx context.Context, event *model.ChangeEvent) (string, error) {
	key, value, ok := operationIdentity(event)
	if !ok {
		return "", nil
	}
	result, err := s.ListCurrent(ctx, model.CurrentParams{
		EventType:        event.EventType,
		CorrelationKey:   key,
		CorrelationValue: value,
		Limit:            1,
	})
	if err != nil {
		return "", err
	}
	if result.TotalCount > 0 {
		return model.OperationStateOpen, nil
	}
	return model.OperationStateClosed, nil
}

// CloseOperation appends an idempotent end event for a correlated start.
func (s *ChangeService) CloseOperation(ctx context.Context, eventID, userName, description string) (*model.ChangeEvent, error) {
	return s.CloseOperationAs(ctx, eventID, model.UserIdentity{Name: userName}, description)
}

// CloseOperationAs appends a correlated end event attributed to an authenticated identity.
func (s *ChangeService) CloseOperationAs(ctx context.Context, eventID string, user model.UserIdentity, description string) (*model.ChangeEvent, error) {
	if user.Name == "" {
		return nil, ErrUserNameRequired
	}
	start, err := s.GetByID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	key, value, ok := operationIdentity(start)
	if !ok {
		return nil, ErrOperationNotClosable
	}
	state, err := s.OperationState(ctx, start)
	if err != nil {
		return nil, err
	}
	if state != model.OperationStateOpen {
		return nil, ErrOperationClosed
	}

	description = strings.TrimSpace(description)
	if description == "" {
		description = start.Description + " completed"
	}
	tags := maps.Clone(start.Tags)
	tags[key] = value
	tags["phase"] = "end"
	// intentional: preserve tags and correlation values from starts accepted
	// before the current new-event tag policy.
	created, err := s.create(ctx, &model.CreateChangeRequest{
		ExternalID:   operationCloseExternalID(start.EventType, key, value),
		UserName:     user.Name,
		UserProvider: user.Provider,
		UserSubject:  user.Subject,
		EventType:    start.EventType,
		Description:  description,
		Tags:         tags,
	}, false)
	if errors.Is(err, store.ErrDuplicate) {
		return created, nil
	}
	return created, err
}

func operationCloseExternalID(eventType, correlationKey, correlationValue string) string {
	payload := eventType + "\x00" + correlationKey + "\x00" + correlationValue
	return fmt.Sprintf("pcr:close:%x", sha256.Sum256([]byte(payload)))
}

func operationIdentity(event *model.ChangeEvent) (string, string, bool) {
	if event == nil || event.ParentID != "" || event.Tags["phase"] != "start" {
		return "", "", false
	}
	if value := event.Tags["change_id"]; value != "" {
		return "change_id", value, true
	}
	if value := event.Tags["deploy_id"]; value != "" {
		return "deploy_id", value, true
	}
	return "", "", false
}

// ToggleStar creates a star or unstar meta-event for the given event.
func (s *ChangeService) ToggleStar(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error) {
	return s.ToggleStarAs(ctx, eventID, model.UserIdentity{Name: userName})
}

// ToggleStarAs appends a star transition attributed to an authenticated identity.
func (s *ChangeService) ToggleStarAs(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	event, err := s.store.ToggleStar(ctx, eventID, user)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrEventNotFound
	}
	return event, err
}

// ToggleAlert creates an alert or clear-alert meta-event for the given event.
func (s *ChangeService) ToggleAlert(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error) {
	return s.ToggleAlertAs(ctx, eventID, model.UserIdentity{Name: userName})
}

// ToggleAlertAs appends an alert transition attributed to an authenticated identity.
func (s *ChangeService) ToggleAlertAs(ctx context.Context, eventID string, user model.UserIdentity) (*model.ChangeEvent, error) {
	event, err := s.store.ToggleAlert(ctx, eventID, user)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrEventNotFound
	}
	return event, err
}
