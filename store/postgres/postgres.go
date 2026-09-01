// Package postgres is a production-shaped store.Store backed by PostgreSQL.
//
// It lives in its own module so the core packages stay dependency-free: a
// partner who only wants the interface never downloads pgx, and never has to
// clear it through a dependency review.
//
//	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
//	if err != nil {
//	    return err
//	}
//	if err := postgres.Migrate(ctx, pool); err != nil {
//	    return err
//	}
//	st := postgres.New(pool, "helppi")
//
// Correctness here rests on one line of schema rather than on Go: the unique
// index on directory_id is what makes creation idempotent when two workers
// race. See migrations/0001_init.sql.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Helppi/helppi-scim-go/store"
)

// Store implements store.Store. It is safe for concurrent use.
//
// It deliberately does NOT implement store.Ephemeral: this store survives a
// restart, which is what makes it safe to point at a real directory.
type Store struct {
	pool    *pgxpool.Pool
	partner string
}

// New wraps an existing pool. partner names the directory whose checkpoint this
// store tracks, so one database can serve more than one.
func New(pool *pgxpool.Pool, partner string) *Store {
	if partner == "" {
		partner = "helppi"
	}
	return &Store{pool: pool, partner: partner}
}

const helpperColumns = `id::text, directory_id, login, display_name, enabled, created_at, updated_at`

func scanHelpper(row pgx.Row) (store.Helpper, error) {
	var h store.Helpper
	err := row.Scan(&h.ID, &h.DirectoryID, &h.Login, &h.DisplayName, &h.Enabled, &h.CreatedAt, &h.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Helpper{}, store.ErrNotFound
	}
	return h, err
}

// HelpperByDirectoryID looks a record up by the directory's identifier — the
// only key the two sides match on.
func (s *Store) HelpperByDirectoryID(ctx context.Context, directoryID string) (store.Helpper, error) {
	return scanHelpper(s.pool.QueryRow(ctx,
		`select `+helpperColumns+` from helppers where directory_id = $1`, directoryID))
}

// CreateHelpper inserts a record, or reports store.ErrAlreadyExists when
// another worker won the race.
//
// "on conflict do nothing returning" gives us that answer without decoding a
// driver error code: a conflict returns no row. If you need to tell which
// constraint fired, catch SQLSTATE 23505 instead.
func (s *Store) CreateHelpper(ctx context.Context, in store.NewHelpper) (store.Helpper, error) {
	h, err := scanHelpper(s.pool.QueryRow(ctx, `
		insert into helppers (directory_id, login, display_name, enabled)
		values ($1, $2, $3, true)
		on conflict (directory_id) do nothing
		returning `+helpperColumns,
		in.DirectoryID, in.Login, in.DisplayName))

	if errors.Is(err, store.ErrNotFound) {
		return store.Helpper{}, store.ErrAlreadyExists
	}
	if err != nil {
		return store.Helpper{}, fmt.Errorf("create helpper %s: %w", in.DirectoryID, err)
	}
	return h, nil
}

// UpdateHelpper writes the desired state of an existing record.
func (s *Store) UpdateHelpper(ctx context.Context, id string, upd store.HelpperUpdate) error {
	// This implementation uses bigint keys, so an id that is not a number
	// cannot exist here. Saying so beats letting Postgres raise a cast error.
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return store.ErrNotFound
	}

	tag, err := s.pool.Exec(ctx, `
		update helppers
		   set enabled = $2, display_name = $3, login = $4, updated_at = now()
		 where id = $1::bigint`,
		id, upd.Enabled, upd.DisplayName, upd.Login)
	if err != nil {
		return fmt.Errorf("update helpper %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// EnabledHelppers lists everyone currently allowed to work. The daily walk
// compares this against the directory to report drift.
func (s *Store) EnabledHelppers(ctx context.Context) ([]store.Helpper, error) {
	rows, err := s.pool.Query(ctx,
		`select `+helpperColumns+` from helppers where enabled order by directory_id`)
	if err != nil {
		return nil, fmt.Errorf("list enabled helppers: %w", err)
	}
	defer rows.Close()

	var out []store.Helpper
	for rows.Next() {
		h, err := scanHelpper(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Checkpoint returns the newest directory timestamp from a cycle that finished.
// A store that has never synchronized returns the zero time, which is what
// tells the reconciler to walk everything.
func (s *Store) Checkpoint(ctx context.Context) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`select checkpoint from directory_sync_state where partner = $1`, s.partner).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read checkpoint: %w", err)
	}
	// 'epoch' is the schema default for a partner that has a row but has never
	// completed a cycle; the reconciler expects the zero time for that.
	if at.Unix() == 0 {
		return time.Time{}, nil
	}
	return at, nil
}

// SetCheckpoint records the watermark. The reconciler calls this only after a
// cycle completes in full.
func (s *Store) SetCheckpoint(ctx context.Context, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		insert into directory_sync_state (partner, checkpoint)
		values ($1, $2)
		on conflict (partner) do update
		   set checkpoint = excluded.checkpoint, updated_at = now()`,
		s.partner, at)
	if err != nil {
		return fmt.Errorf("write checkpoint: %w", err)
	}
	return nil
}
