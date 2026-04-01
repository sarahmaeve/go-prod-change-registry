package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/sarah/go-prod-change-registry/internal/model"
	"github.com/sarah/go-prod-change-registry/internal/store"
)

var (
	ErrUserNameRequired  = errors.New("user_name is required")
	ErrEventTypeRequired = errors.New("event_type is required")
	ErrEventNotFound     = errors.New("event not found")
	ErrParentNotFound    = errors.New("parent event not found")
)

type ChangeService struct {
	store store.ChangeStore
}

func NewChangeService(store store.ChangeStore) *ChangeService {
	return &ChangeService{store: store}
}

func (s *ChangeService) Create(ctx context.Context, req *model.CreateChangeRequest) (*model.ChangeEvent, error) {
	if req.UserName == "" {
		return nil, ErrUserNameRequired
	}
	if req.EventType == "" {
		return nil, ErrEventTypeRequired
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
		ts = *req.Timestamp
	}

	tags := req.Tags
	if tags == nil {
		tags = make(map[string]string)
	}

	event := &model.ChangeEvent{
		ID:              uuid.Must(uuid.NewV7()).String(),
		ParentID:        req.ParentID,
		UserName:        req.UserName,
		Timestamp:       ts,
		EventType:       req.EventType,
		Description:     req.Description,
		LongDescription: req.LongDescription,
		Tags:            tags,
		CreatedAt:       now,
	}

	return s.store.Create(ctx, event)
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

func (s *ChangeService) GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error) {
	return s.store.GetAnnotations(ctx, eventID)
}

func (s *ChangeService) GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error) {
	return s.store.GetAnnotationsBatch(ctx, eventIDs)
}

// ToggleStar creates a star or unstar meta-event for the given event.
func (s *ChangeService) ToggleStar(ctx context.Context, eventID, userName string) (*model.ChangeEvent, error) {
	// Verify parent exists.
	parent, err := s.store.GetByID(ctx, eventID)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, ErrEventNotFound
	}

	// Check current annotation state.
	annotations, err := s.store.GetAnnotations(ctx, eventID)
	if err != nil {
		return nil, err
	}

	eventType := model.EventTypeStar
	description := "starred"
	if annotations != nil && annotations.Starred {
		eventType = model.EventTypeUnstar
		description = "unstarred"
	}

	now := time.Now().UTC()
	metaEvent := &model.ChangeEvent{
		ID:          uuid.Must(uuid.NewV7()).String(),
		ParentID:    eventID,
		UserName:    userName,
		Timestamp:   now,
		EventType:   eventType,
		Description: description,
		Tags:        make(map[string]string),
		CreatedAt:   now,
	}

	return s.store.Create(ctx, metaEvent)
}
