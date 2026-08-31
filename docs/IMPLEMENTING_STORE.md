# Implementing `store.Store`

This is the only code the integration requires you to write. The interface is
six methods, and most of its rules are invisible in the signatures — so there is
a contract test suite that checks them for you.

## Run the contract suite first

```go
func TestMyStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store {
        return newTestStore(t) // fresh, empty, isolated per call
    })
}
```

It checks the things that are easy to get subtly wrong: that a missing picker
returns `ErrNotFound` rather than a zero value, that a duplicate `directory_id`
returns `ErrAlreadyExists`, that eight concurrent creates produce exactly one
picker, that `EnabledPickers` excludes disabled ones, and that a checkpoint
survives a round trip with its time zone intact.

## The schema

`deploy/schema.sql` is the reference. Two things in it are load-bearing:

```sql
constraint pickers_directory_id_key unique (directory_id)
```

This is what actually makes creation idempotent. The reconciler's "does it
exist?" lookup is a fast path, not a guarantee — two workers can both miss it
and both proceed. Under a unique index the loser gets a constraint violation,
which you translate to `ErrAlreadyExists`, and the reconciler re-reads and
continues. Without the index, you get two pickers for one person and a `409`
storm on write-back.

```sql
checkpoint timestamptz not null default 'epoch'
```

Time zone matters: the checkpoint is compared against directory timestamps.
Storing it as a naive local timestamp reintroduces exactly the clock problem
the design removed. A fresh store must report the **zero time**, which is what
tells the reconciler to walk everything on the first run.

## Translating errors

```go
func (s *PostgresStore) CreatePicker(ctx context.Context, p store.NewPicker) (store.Picker, error) {
    var out store.Picker
    err := s.db.QueryRow(ctx, `
        insert into pickers (directory_id, login, display_name, enabled)
        values ($1, $2, $3, true)
        returning id::text, directory_id, login, display_name, enabled
    `, p.DirectoryID, p.Login, p.DisplayName).Scan(
        &out.ID, &out.DirectoryID, &out.Login, &out.DisplayName, &out.Enabled)

    var pgErr *pgconn.PgError
    if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
        return store.Picker{}, store.ErrAlreadyExists
    }
    if errors.Is(err, pgx.ErrNoRows) {
        return store.Picker{}, store.ErrNotFound
    }
    return out, err
}
```

Return the sentinel errors themselves, or wrap them so `errors.Is` still finds
them. The reconciler branches on `ErrNotFound` to decide whether to create, and
on `ErrAlreadyExists` to decide whether to re-read: a driver error that does not
match either is treated as a real failure and stops the cycle.

## Picker identifiers

`Picker.ID` is a string so a UUID, a ULID or a bigint all fit — format it
however your system already identifies pickers. Two requirements: it must be
stable for the life of the account, and it must never be reassigned. It is
written into Helppi's directory as `externalId`, and both sides use the pair
afterwards.

If your primary key is an integer, `id::text` in the query above is enough.

## Do not implement `Ephemeral`

That marker exists so the worker can refuse to run a throwaway store against a
real directory. A database-backed store should not implement it.
