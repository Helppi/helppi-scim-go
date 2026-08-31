// Package store is the local side of the integration: pickers and the sync
// checkpoint. The reconciler talks only to these interfaces, so a real
// implementation (Postgres, DynamoDB, whatever you already run) can replace the
// in-memory one without touching sync logic.
//
// Implementing Store is the only code this integration actually requires you to
// write. See the worked example in example_test.go.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when no picker matches the directory id.
	ErrNotFound = errors.New("store: not found")
	// ErrAlreadyExists is returned when a concurrent worker won the race to
	// create the same directory id. The caller re-reads rather than retrying.
	ErrAlreadyExists = errors.New("store: already exists")
)

// Picker is the local account.
type Picker struct {
	// ID is your identifier for this account — the value written back to the
	// directory as externalId, which the proposal calls picker_id.
	//
	// It is a string so that a UUID, a ULID or a numeric key all fit; format it
	// however your system already identifies pickers. It must be stable and
	// never reassigned.
	ID string

	// DirectoryID is the identifier issued by Helppi. It is the only key the
	// two sides match on — never login, never name.
	DirectoryID string

	Login       string // alias published by the directory (@separador.app)
	DisplayName string // abbreviated name, e.g. "Marcio C."
	Enabled     bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewPicker is the payload for a creation.
type NewPicker struct {
	DirectoryID string
	Login       string
	DisplayName string
}

// PickerUpdate is the desired state of an existing picker.
//
// It is a struct rather than positional arguments because DisplayName and Login
// are both strings: passed positionally, swapping them compiles cleanly and
// fails silently in production.
type PickerUpdate struct {
	Enabled     bool
	DisplayName string
	Login       string
}

// Store is everything the reconciler needs.
//
// CreatePicker must be idempotent under concurrency: enforce uniqueness on
// DirectoryID in the database (a unique index), not in application code. The
// ErrNotFound check in the reconciler is a fast path, not a guarantee — two
// workers can both miss it.
type Store interface {
	PickerByDirectoryID(ctx context.Context, directoryID string) (Picker, error)
	CreatePicker(ctx context.Context, p NewPicker) (Picker, error)
	UpdatePicker(ctx context.Context, id string, upd PickerUpdate) error
	EnabledPickers(ctx context.Context) ([]Picker, error)

	Checkpoint(ctx context.Context) (time.Time, error)
	SetCheckpoint(ctx context.Context, at time.Time) error
}

// Ephemeral is an optional marker for stores that lose their contents when the
// process exits, such as the in-memory reference implementation.
//
// The worker refuses to run against a real directory with an ephemeral store,
// because an empty store makes every record look new: it would mint fresh
// picker ids and write them over the real ones. Implementations backed by a
// database should not implement this interface at all.
type Ephemeral interface {
	Ephemeral() bool
}

// IsEphemeral reports whether s loses its contents on restart.
func IsEphemeral(s Store) bool {
	e, ok := s.(Ephemeral)
	return ok && e.Ephemeral()
}
