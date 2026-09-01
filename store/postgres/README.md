# store/postgres

A production-shaped `store.Store` backed by PostgreSQL.

It is a **separate Go module on purpose**. The core packages (`scim`,
`directory`, `store`, `scimtest`, `conformance`) have no third-party
dependencies at all, so a partner who only wants the interface never downloads
pgx and never has to clear it through a dependency review. Opting into this
package is the only way to acquire a dependency here.

```go
pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
if err != nil {
    return err
}
if err := postgres.Migrate(ctx, pool); err != nil {
    return err
}

st := postgres.New(pool, "helppi")
syncer := directory.New(client, st, directory.Options{})
```

## Run one instance, and let the database enforce it

```go
err := st.WithSyncLock(ctx, func(ctx context.Context) error {
    _, err := syncer.Incremental(ctx)
    return err
})
if errors.Is(err, postgres.ErrLockHeld) {
    log.Info("another instance is running this cycle; skipping")
    return
}
```

Two replicas on the same schedule both try to create helppers on the first
sync. The unique index prevents duplicates, but the resulting `409` storm is
avoidable. The lock is session-level and tied to one pooled connection, so the
database releases it even if the process dies.

## What carries the correctness

`migrations/0001_init.sql`, not Go:

```sql
constraint helppers_directory_id_key unique (directory_id)
```

The reconciler's "does this helpper exist?" lookup is a fast path, not a
guarantee — two workers can both miss it and both proceed. Under the unique
index the loser gets no row back from `insert … on conflict do nothing
returning`, which this store reports as `store.ErrAlreadyExists`, and the
reconciler re-reads and continues.

`ConcurrentCreateYieldsExactlyOne` in the contract suite is that claim, checked
against a real database.

## Tests

The contract suite runs against this implementation, so it is held to the same
rules as the in-memory one:

```bash
createdb helppi_scim_go_test
DATABASE_URL=postgres://postgres@localhost:5432/helppi_scim_go_test?sslmode=disable \
    go test -tags integration -race ./...
```

They are behind a build tag, so the repository's default `go test ./...` stays
offline and instant.

## Two things to know

**Go 1.24, not 1.22.** The core module's floor is Go 1.22; pgx's dependency
chain needs 1.24. That asymmetry is precisely why this is a separate module:
the floor only rises for people who opt in.

**Not consumable until the parent is tagged.** `go.mod` carries a `replace`
pointing at the working tree, because `github.com/Helppi/helppi-scim-go` has no
released version yet. The directive is ignored by anyone importing this module,
so it does not leak into your build — but it does mean this module resolves
only inside this repository until `v0.1.0` exists upstream.
