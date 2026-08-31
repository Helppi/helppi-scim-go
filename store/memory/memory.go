// Package memory is a reference Store for tests and for running the worker
// against a directory without provisioning a database.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/Helppi/helppi-scim-go/store"
)

// Store keeps pickers in memory. Safe for concurrent use.
type Store struct {
	mu         sync.RWMutex
	byDirID    map[string]*store.Picker
	nextID     int64
	checkpoint time.Time
	now        func() time.Time
}

// New builds an empty Store. Pass nil for now to use time.Now.
func New(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{byDirID: map[string]*store.Picker{}, nextID: 900000, now: now}
}

func (s *Store) PickerByDirectoryID(_ context.Context, directoryID string) (store.Picker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byDirID[directoryID]
	if !ok {
		return store.Picker{}, store.ErrNotFound
	}
	return *p, nil
}

func (s *Store) CreatePicker(_ context.Context, in store.NewPicker) (store.Picker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byDirID[in.DirectoryID]; ok {
		// Mirrors a unique-index violation in a real database.
		return store.Picker{}, store.ErrAlreadyExists
	}
	s.nextID++
	now := s.now()
	p := &store.Picker{
		ID:          s.nextID,
		DirectoryID: in.DirectoryID,
		Login:       in.Login,
		DisplayName: in.DisplayName,
		Enabled:     true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.byDirID[in.DirectoryID] = p
	return *p, nil
}

func (s *Store) UpdatePicker(_ context.Context, id int64, enabled bool, displayName, login string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.byDirID {
		if p.ID != id {
			continue
		}
		p.Enabled, p.DisplayName, p.Login, p.UpdatedAt = enabled, displayName, login, s.now()
		return nil
	}
	return store.ErrNotFound
}

func (s *Store) EnabledPickers(_ context.Context) ([]store.Picker, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.Picker, 0, len(s.byDirID))
	for _, p := range s.byDirID {
		if p.Enabled {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirectoryID < out[j].DirectoryID })
	return out, nil
}

func (s *Store) Checkpoint(_ context.Context) (time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.checkpoint, nil
}

func (s *Store) SetCheckpoint(_ context.Context, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint = at
	return nil
}

// All returns every picker, ordered by directory id. Test helper.
func (s *Store) All() []store.Picker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.Picker, 0, len(s.byDirID))
	for _, p := range s.byDirID {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirectoryID < out[j].DirectoryID })
	return out
}
