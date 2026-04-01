package store

import (
	"context"

	"github.com/sarah/go-prod-change-registry/internal/model"
)

// ChangeStore defines the persistence operations for change events.
type ChangeStore interface {
	Create(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	GetByID(ctx context.Context, id string) (*model.ChangeEvent, error)
	Update(ctx context.Context, event *model.ChangeEvent) (*model.ChangeEvent, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, params model.ListParams) (*model.ListResult, error)
	Close() error
}
