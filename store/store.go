// Package store is the local side of the integration: pickers and the sync
// checkpoint. The reconciler talks only to these interfaces, so a real
// implementation (Postgres, DynamoDB, whatever the partner already uses) can replace
// the in-memory one without touching sync logic.
package store

import (
	"context"
	"errors"
	"strconv"
	"time"
)

var (
	// ErrNotFound is returned when no picker matches the directory id.
	ErrNotFound = errors.New("store: not found")
	// ErrAlreadyExists is returned when a concurrent worker won the race to
	// create the same directory id. The caller must re-read, not retry blindly.
	ErrAlreadyExists = errors.New("store: already exists")
)

// Picker is the local account. ID is what the directory knows as picker_id.
type Picker struct {
	ID          int64
	DirectoryID string
	Login       string
	DisplayName string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// PickerID renders the local id the way it is written back to externalId.
func (p Picker) PickerID() string { return strconv.FormatInt(p.ID, 10) }

// NewPicker is the payload for a creation.
type NewPicker struct {
	DirectoryID string
	Login       string
	DisplayName string
}

// Store is everything the reconciler needs.
//
// CreatePicker must be idempotent under concurrency: enforce uniqueness on
// DirectoryID in the database (unique index), not in application code. The
// ErrNotFound check in the reconciler is a fast path, not a guarantee.
type Store interface {
	PickerByDirectoryID(ctx context.Context, directoryID string) (Picker, error)
	CreatePicker(ctx context.Context, p NewPicker) (Picker, error)
	UpdatePicker(ctx context.Context, id int64, enabled bool, displayName, login string) error
	EnabledPickers(ctx context.Context) ([]Picker, error)

	Checkpoint(ctx context.Context) (time.Time, error)
	SetCheckpoint(ctx context.Context, at time.Time) error
}
