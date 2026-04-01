package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

// Validation errors returned by ChangeService methods.
var (
	ErrUserNameRequired = errors.New("user_name is required")
	ErrEventNotFound    = errors.New("event not found")
)

// ChangeService implements business logic for production change events.
type ChangeService struct {
	store store.ChangeStore
}

// NewChangeService returns a new ChangeService backed by the given store.
func NewChangeService(store store.ChangeStore) *ChangeService {
	return &ChangeService{store: store}
}

// Create validates the request, populates defaults, and persists a new change event.
func (s *ChangeService) Create(ctx context.Context, req *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	if req.UserName == "" {
		return nil, ErrUserNameRequired
	}

	now := time.Now().UTC()

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	tsStart := now
	if req.TimestampStart != nil {
		tsStart = *req.TimestampStart
	}

	tags := req.Tags
	if tags == nil {
		tags = make(map[string]string)
	}

	event := &model.ChangeEvent{
		ID:              id.String(),
		UserName:        req.UserName,
		TimestampStart:  tsStart,
		TimestampEnd:    req.TimestampEnd,
		EventType:       req.EventType,
		Description:     req.Description,
		LongDescription: req.LongDescription,
		Tags:            tags,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	return s.store.Create(ctx, event)
}

// GetByID retrieves a single change event by its ID.
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

// Update applies a partial update to an existing change event.
func (s *ChangeService) Update(ctx context.Context, id string, req *model.UpdateChangeRequest) (*model.ChangeEvent, error) {
	existing, err := s.store.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, ErrEventNotFound
	}

	if req.UserName != nil {
		existing.UserName = *req.UserName
	}
	if req.EventType != nil {
		existing.EventType = *req.EventType
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.LongDescription != nil {
		existing.LongDescription = *req.LongDescription
	}
	if req.TimestampStart != nil {
		existing.TimestampStart = *req.TimestampStart
	}
	if req.TimestampEnd != nil {
		existing.TimestampEnd = req.TimestampEnd
	}
	if req.Tags != nil {
		existing.Tags = *req.Tags
	}
	if req.Starred != nil {
		existing.Starred = *req.Starred
	}
	if req.Alerted != nil {
		existing.Alerted = *req.Alerted
	}

	existing.UpdatedAt = time.Now().UTC()

	return s.store.Update(ctx, existing)
}

// Delete removes a change event by its ID.
func (s *ChangeService) Delete(ctx context.Context, id string) error {
	return s.store.Delete(ctx, id)
}

// List returns a paginated list of change events. Limit is clamped to [1, 200]
// and defaults to 50 when zero.
func (s *ChangeService) List(ctx context.Context, params model.ListParams) (*model.ListResult, error) {
	if params.Limit <= 0 {
		params.Limit = model.DefaultLimit
	}
	if params.Limit > 200 {
		params.Limit = 200
	}

	return s.store.List(ctx, params)
}
