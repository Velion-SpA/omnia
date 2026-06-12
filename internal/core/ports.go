package core

import (
	"context"
	"time"
)

// Source fetches items from an external system.
type Source interface {
	// Name returns a human-readable identifier for the source.
	Name() string
	// Fetch retrieves items updated/created since the given time.
	Fetch(ctx context.Context, since time.Time) ([]Item, error)
}

// Sink writes items to a destination store.
type Sink interface {
	// Write persists an item. Implementations must handle upsert via topic_key.
	Write(ctx context.Context, item Item) error
	// Health checks if the sink is reachable.
	Health(ctx context.Context) error
}

// StateStore persists incremental sync cursors.
type StateStore interface {
	// GetCursor returns the last-processed cursor for the given source+key.
	GetCursor(source, key string) (string, bool)
	// SetCursor persists a cursor value.
	SetCursor(source, key, value string) error
	// Flush writes any pending state changes to disk.
	Flush() error
}
