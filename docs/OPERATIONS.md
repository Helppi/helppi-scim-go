# Running the sync worker

## Before the first real run

The worker refuses to start against a real directory with the in-memory store,
and that guard is the most important line in this repository. An empty store
makes every directory record look new: the worker would create fresh helppers and
`PATCH` their invented ids over the real `externalId` values — corrupting data on
Helppi's side, not yours.

So the first run is always a dry run:

```bash
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… go run ./cmd/directorysyncd -once -dry-run
```

A dry run writes nothing: not to your store, not to the directory, not even to
the checkpoint. It reports what a real cycle would do. Read those numbers before
you plug in a real store.

## Exactly one instance

Two replicas on the same schedule both try to create helppers on the first sync.
The unique index on `directory_id` prevents duplicates, but you get a burst of
`409`s and a confusing first day. Pick one:

- a Postgres advisory lock around the cycle (`store/postgres` exposes it as
  `WithSyncLock`, which returns `ErrLockHeld` when someone else is running);
- a lease in whatever coordination service you already run;
- a Kubernetes `CronJob` with `concurrencyPolicy: Forbid`, using `-once`.

## Metrics

Exposed at `-metrics-addr` (default `:9090`), plus `/healthz` (process is up)
and `/readyz` (at least one cycle has completed).

| Metric | What it tells you |
|---|---|
| `directory_sync_lag_seconds` | Age of the checkpoint. **The one that matters.** |
| `directory_sync_cycles_total` / `_failures_total` | Cycle throughput and failure rate |
| `directory_helppers_created_total` | Creations; a spike means an onboarding wave — or an empty store |
| `directory_helppers_disabled_total` | Disables applied; this is the access-removal path |
| `directory_write_backs_total` | Identifiers returned to the directory as `externalId` |
| `directory_write_back_conflicts_total` | Mapping conflicts; should be zero |
| `directory_malformed_records_total` | Records the directory served that we could not use |
| `directory_alerts_total` | Conditions no retry can fix |

`directory_sync_lag_seconds` is the SLI behind "a termination reaches the
partner within N minutes". Alert on it above roughly three cycle intervals. It
is the only metric that catches a worker that is stuck rather than failing —
a hung cycle produces no errors at all.

## Runbook

**Lag is climbing, no errors.** The worker is stuck, not broken. Check that
exactly one instance holds the lock and that the last cycle actually finished;
a cycle that exceeds `-cycle-timeout` is aborted and retried, so a growing lag
with no failures usually means a very slow directory or a lock held by a dead
process.

**`401` or `403` on every request.** The credential was revoked, rotated or
never had the scope. The client does not retry these, on purpose: retrying a
rejected credential just fills someone's log. Fix the token and restart.

**A `409` alert.** Your identifier is already bound to a different directory
identity. Almost always a duplicate helpper created before the unique index
existed. Find both accounts, decide which survives, repoint it. The alert fires
once per identity per process, so a restart tells you whether it is still there.

**Drift is non-empty after the daily walk.** `should_be_disabled` or
`missing_external_id` mean the incremental path dropped something — check for
failed cycles in the window. `absent_from_directory` is different: those people
passed the retention window. Confirm they are already disabled locally; if one
is still enabled, that is a real miss and worth understanding.

**`malformed` is non-zero.** The directory served records this client cannot
use — most often no `active` flag. They are skipped, and the checkpoint is held
behind the oldest of them, so nothing is lost once the feed is repaired. Report
it to Helppi with the ids from the alert. If it exceeds the tolerance the cycle
fails outright, which is the intended signal that the feed itself is broken.

**A cycle failed once.** Do nothing. The checkpoint did not move; the next cycle
redoes exactly that work. That is the whole point of a reconciler.

## Configuration

| Flag | Default | Notes |
|---|---|---|
| `-incremental` | `5m` | How quickly a termination propagates |
| `-full` | `24h` | The drift-detection walk |
| `-page-size` | `200` | Balance between round trips and retry cost |
| `-rps` | `5` | Client-side throttle |
| `-cycle-timeout` | `30m` | Aborts a hung cycle so the next one can run |
| `-dry-run` | off | Writes nothing, anywhere |
| `-once` | off | One full reconciliation, then exit — for cron |
| `-allow-ephemeral-store` | off | Removes the safety guard. Only for a throwaway directory |
