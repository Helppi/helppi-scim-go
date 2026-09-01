package postgres

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
)

// ErrLockHeld is returned when another process is already running a cycle.
var ErrLockHeld = errors.New("postgres: the sync lock is held by another process")

// WithSyncLock runs fn while holding a session-level advisory lock, and returns
// ErrLockHeld immediately if someone else has it.
//
// Run exactly one sync worker. Two replicas on the same schedule both try to
// create helppers on the first sync; the unique index prevents duplicates, but
// the resulting 409 storm is avoidable. Wrap each cycle in this:
//
//	err := st.WithSyncLock(ctx, func(ctx context.Context) error {
//	    _, err := syncer.Incremental(ctx)
//	    return err
//	})
//	if errors.Is(err, postgres.ErrLockHeld) {
//	    log.Info("another instance is running this cycle; skipping")
//	    return
//	}
//
// The lock is tied to one pooled connection and released when fn returns, or
// by the database itself if the process dies.
func (s *Store) WithSyncLock(ctx context.Context, fn func(context.Context) error) error {
	key := lockKey("directory_sync:" + s.partner)

	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("sync lock: acquire connection: %w", err)
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, "select pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		return fmt.Errorf("sync lock: %w", err)
	}
	if !acquired {
		return ErrLockHeld
	}
	defer func() {
		// Release even when ctx is already done, or the lock lingers until the
		// connection is recycled.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", key)
	}()

	return fn(ctx)
}

// lockKey hashes in Go rather than calling Postgres's hashtext(), which is an
// internal function with no compatibility promise.
func lockKey(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() >> 1) // stay positive; the sign carries no meaning
}
