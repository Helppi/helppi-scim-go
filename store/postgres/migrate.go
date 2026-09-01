package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLockKey keeps two processes from migrating at the same moment. Any
// stable constant works; this one is arbitrary and namespaced to this schema.
const migrationLockKey int64 = 8_531_204_771_001

// Migrate applies every pending migration, in filename order, inside a
// transaction each. It is safe to call on every boot and safe to call
// concurrently: the first caller holds an advisory lock and the others wait.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migrate: take lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock($1)", migrationLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		create table if not exists schema_migrations (
			version    text        primary key,
			applied_at timestamptz not null default now()
		)`); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, file := range files {
		var applied bool
		if err := conn.QueryRow(ctx,
			"select exists (select 1 from schema_migrations where version = $1)", file,
		).Scan(&applied); err != nil {
			return fmt.Errorf("migrate: check %s: %w", file, err)
		}
		if applied {
			continue
		}

		body, err := migrations.ReadFile(file)
		if err != nil {
			return err
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: begin %s: %w", file, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: apply %s: %w", file, err)
		}
		if _, err := tx.Exec(ctx,
			"insert into schema_migrations (version) values ($1)", file); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", file, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", file, err)
		}
	}
	return nil
}
