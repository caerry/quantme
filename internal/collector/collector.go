package collector

import (
	"context"
	"quantme/internal/event"
	"time"
)

// Collector defines the interface for metric collectors
type Collector interface {
	Start(ctx context.Context, interval time.Duration, output chan<- event.Event, focusOnly bool) error
	Stop() error
	// GetCurrentFocus could be useful for immediate checks if needed
	GetCurrentFocus() (event.FocusInfo, error)
}
