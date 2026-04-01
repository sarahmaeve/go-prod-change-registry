package store

import (
	"context"
	"errors"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// ErrNotFound is returned when a requested entity does not exist.
var ErrNotFound = errors.New("not found")

// ErrDuplicate is returned when an event with the same external_id already exists.
// The caller receives the existing event alongside this sentinel error.
var ErrDuplicate = errors.New("duplicate external_id")

// ChangeStore defines the persistence interface for change events.
// Events are append-only — no Update or Delete operations.
type ChangeStore interface {
	Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	GetByID(ctx context.Context, id string) (*model.ChangeEvent, error)
	List(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	GetAnnotations(ctx context.Context, eventID string) (*model.EventAnnotations, error)
	GetAnnotationsBatch(ctx context.Context, eventIDs []string) (map[string]*model.EventAnnotations, error)
	Close() error
}
