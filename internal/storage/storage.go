package storage

import (
	"context"
	"quantme/internal/event"
	"time"
)

type Storage interface {
	Init(ctx context.Context) error
	SaveEvent(ctx context.Context, e event.Event) (int64, error)
	GetEvents(ctx context.Context, start, end time.Time, eventTypes ...event.EventType) ([]event.Event, error)
	Close() error
}
