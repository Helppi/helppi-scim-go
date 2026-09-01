//go:build integration

// These tests need a real PostgreSQL. They are behind a build tag so the
// default `go test ./...` stays offline and instant:
//
//	DATABASE_URL=postgres://postgres@localhost:5432/helppi_scim_go_test?sslmode=disable \
//	    go test -tags integration ./...
package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Helppi/helppi-scim-go/store"
	"github.com/Helppi/helppi-scim-go/store/postgres"
	"github.com/Helppi/helppi-scim-go/store/storetest"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("set DATABASE_URL to run the PostgreSQL integration tests")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	if err := postgres.Migrate(context.Background(), p); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return p
}

func truncate(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	if _, err := p.Exec(context.Background(),
		"truncate helppers, directory_sync_state restart identity"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// The whole point of the contract suite: the implementation a partner will
// actually copy has to pass the same checks as the in-memory one.
func TestPostgresStoreSatisfiesTheContract(t *testing.T) {
	p := pool(t)
	storetest.Run(t, func(t *testing.T) store.Store {
		truncate(t, p)
		return postgres.New(p, "helppi")
	})
}

func TestMigrateIsIdempotent(t *testing.T) {
	p := pool(t)
	// pool() already migrated once; a second and third pass must be no-ops,
	// because this runs on every boot.
	for i := 0; i < 2; i++ {
		if err := postgres.Migrate(context.Background(), p); err != nil {
			t.Fatalf("migrate pass %d: %v", i+2, err)
		}
	}

	var applied int
	if err := p.QueryRow(context.Background(),
		"select count(*) from schema_migrations").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Errorf("schema_migrations holds %d rows, want 1", applied)
	}
}

func TestSyncLockIsExclusive(t *testing.T) {
	p := pool(t)
	truncate(t, p)
	st := postgres.New(p, "helppi")

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- st.WithSyncLock(context.Background(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	// A second worker on the same schedule must be told to stand down, not
	// silently proceed and create a duplicate helpper.
	err := st.WithSyncLock(context.Background(), func(context.Context) error {
		t.Error("the second holder ran the cycle body while the lock was held")
		return nil
	})
	if !errors.Is(err, postgres.ErrLockHeld) {
		t.Fatalf("second acquire = %v, want ErrLockHeld", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first holder: %v", err)
	}

	// And the lock is free again once the first cycle finished.
	if err := st.WithSyncLock(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestSyncLockReleasesWhenTheCycleFails(t *testing.T) {
	p := pool(t)
	st := postgres.New(p, "helppi")

	boom := errors.New("cycle failed")
	if err := st.WithSyncLock(context.Background(), func(context.Context) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the cycle's own error", err)
	}
	if err := st.WithSyncLock(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("a failed cycle left the lock held: %v", err)
	}
}

func TestANonNumericIDIsNotFound(t *testing.T) {
	p := pool(t)
	truncate(t, p)
	st := postgres.New(p, "helppi")

	// This implementation uses bigint keys, so a UUID cannot exist here. It
	// should say so, not surface a Postgres cast error.
	err := st.UpdateHelpper(context.Background(), "not-a-number", store.HelpperUpdate{Enabled: true})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
}

func TestCheckpointIsScopedToThePartner(t *testing.T) {
	p := pool(t)
	truncate(t, p)

	one := postgres.New(p, "partner-one")
	two := postgres.New(p, "partner-two")
	ctx := context.Background()

	at := time.Date(2026, 8, 27, 20, 15, 32, 0, time.UTC)
	if err := one.SetCheckpoint(ctx, at); err != nil {
		t.Fatal(err)
	}

	got, err := one.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Errorf("partner-one checkpoint = %s, want %s", got, at)
	}

	// One database can serve several directories; their watermarks must not
	// bleed into each other.
	other, err := two.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !other.IsZero() {
		t.Errorf("partner-two checkpoint = %s, want the zero time", other)
	}
}
