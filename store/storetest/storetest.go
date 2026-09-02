// Package storetest is a contract test suite for store.Store implementations.
//
// Implementing store.Store is the only code this integration requires you to
// write, and most of its rules are invisible in the type signature: which error
// a missing helpper returns, what happens when two workers create the same
// directory id, whether a checkpoint survives a round trip. Run this suite
// against your implementation and those rules are checked rather than assumed:
//
//	func TestMyPostgresStore(t *testing.T) {
//	    storetest.Run(t, func(t *testing.T) store.Store {
//	        return newTestStore(t) // fresh, empty, isolated
//	    })
//	}
package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Helppi/helppi-scim-go/store"
)

// Factory returns a fresh, empty store for one test.
type Factory func(t *testing.T) store.Store

// Run executes the whole contract suite against the implementation.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	t.Run("MissingHelpperIsNotFound", func(t *testing.T) { missingIsNotFound(t, newStore) })
	t.Run("CreateThenRead", func(t *testing.T) { createThenRead(t, newStore) })
	t.Run("DuplicateDirectoryIDIsRejected", func(t *testing.T) { duplicateRejected(t, newStore) })
	t.Run("ConcurrentCreateYieldsExactlyOne", func(t *testing.T) { concurrentCreate(t, newStore) })
	t.Run("UpdateAppliesEveryField", func(t *testing.T) { updateApplies(t, newStore) })
	t.Run("UpdateOfMissingHelpperIsNotFound", func(t *testing.T) { updateMissing(t, newStore) })
	t.Run("EnabledHelppersExcludesDisabled", func(t *testing.T) { enabledExcludesDisabled(t, newStore) })
	t.Run("CheckpointRoundTrips", func(t *testing.T) { checkpointRoundTrips(t, newStore) })
	t.Run("CheckpointStartsZero", func(t *testing.T) { checkpointStartsZero(t, newStore) })
}

func newHelpper(id string) store.NewHelpper {
	return store.NewHelpper{DirectoryID: id, Login: id + "@separador.app", DisplayName: "Test " + id}
}

func missingIsNotFound(t *testing.T, newStore Factory) {
	s := newStore(t)
	_, err := s.HelpperByDirectoryID(context.Background(), "hlp_nobody")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound — the reconciler branches on it to decide whether to create", err)
	}
}

func createThenRead(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	created, err := s.CreateHelpper(ctx, newHelpper("hlp_a"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("CreateHelpper must return the assigned id: it is written back to the directory as externalId")
	}
	if !created.Enabled {
		t.Error("a newly created helpper must be enabled: it is only created for an active identity")
	}

	got, err := s.HelpperByDirectoryID(ctx, "hlp_a")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ID != created.ID || got.DirectoryID != "hlp_a" {
		t.Errorf("read back %+v, want the record just created (%+v)", got, created)
	}
	if got.Login != "hlp_a@separador.app" || got.DisplayName != "Test hlp_a" {
		t.Errorf("read back %+v, want login and display name preserved", got)
	}
}

func duplicateRejected(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.CreateHelpper(ctx, newHelpper("hlp_dup")); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := s.CreateHelpper(ctx, newHelpper("hlp_dup"))
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second create returned %v, want store.ErrAlreadyExists. Enforce this with a "+
			"unique index on directory_id — the reconciler's lookup is a fast path, not a guarantee", err)
	}
}

func concurrentCreate(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		ids     = map[string]bool{}
		unknown []error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.CreateHelpper(ctx, newHelpper("hlp_race"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				ids[p.ID] = true
			case errors.Is(err, store.ErrAlreadyExists):
			default:
				unknown = append(unknown, err)
			}
		}()
	}
	wg.Wait()

	if len(unknown) > 0 {
		t.Fatalf("concurrent creates produced unexpected errors: %v", unknown)
	}
	if wins != 1 {
		t.Fatalf("%d concurrent creates succeeded, want exactly 1 — two workers must never "+
			"mint two helppers for one identity", wins)
	}
	if len(ids) != 1 {
		t.Errorf("distinct ids assigned = %d, want 1", len(ids))
	}
}

func updateApplies(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	p, err := s.CreateHelpper(ctx, newHelpper("hlp_upd"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	err = s.UpdateHelpper(ctx, p.ID, store.HelpperUpdate{
		Enabled: false, DisplayName: "Renamed R.", Login: "new@separador.app",
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.HelpperByDirectoryID(ctx, "hlp_upd")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Enabled {
		t.Error("Enabled was not applied — this is the field that blocks a closed account")
	}
	if got.DisplayName != "Renamed R." || got.Login != "new@separador.app" {
		t.Errorf("read back %+v, want display name and login updated", got)
	}
	if got.ID != p.ID || got.DirectoryID != "hlp_upd" {
		t.Errorf("update changed identity: %+v", got)
	}
}

func updateMissing(t *testing.T, newStore Factory) {
	s := newStore(t)
	err := s.UpdateHelpper(context.Background(), "no-such-id", store.HelpperUpdate{Enabled: true})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func enabledExcludesDisabled(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	on, err := s.CreateHelpper(ctx, newHelpper("hlp_on"))
	if err != nil {
		t.Fatal(err)
	}
	off, err := s.CreateHelpper(ctx, newHelpper("hlp_off"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateHelpper(ctx, off.ID, store.HelpperUpdate{
		Enabled: false, DisplayName: "Test hlp_off", Login: "hlp_off@separador.app",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := s.EnabledHelppers(ctx)
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(list) != 1 || list[0].ID != on.ID {
		t.Fatalf("EnabledHelppers = %+v, want only %s. The daily walk compares this list "+
			"against the directory to report drift", list, on.ID)
	}
}

func checkpointRoundTrips(t *testing.T, newStore Factory) {
	s := newStore(t)
	ctx := context.Background()

	want := time.Date(2026, 8, 27, 20, 15, 32, 0, time.UTC)
	if err := s.SetCheckpoint(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("checkpoint = %s, want %s. Store it with time zone and at least second "+
			"precision: it is compared against directory timestamps", got, want)
	}
}

func checkpointStartsZero(t *testing.T, newStore Factory) {
	s := newStore(t)
	cp, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !cp.IsZero() {
		t.Fatalf("a fresh store returned checkpoint %s, want the zero time — that is what "+
			"tells the reconciler to do a full first walk", cp)
	}
}
