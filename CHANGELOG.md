# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the version is `0.x`, the public API may still change between minor
versions. Anything that would break a caller is listed under **Changed** with
the migration in the same bullet.

## [Unreleased]

### Added

- `store/postgres`: a PostgreSQL `store.Store` in its own Go module, so the
  core packages stay dependency-free and only people who want it download pgx.
  Ships the migration, passes the `storetest` contract suite against a real
  database, and exposes `WithSyncLock` — a session-level advisory lock that
  returns `ErrLockHeld` — for the single-instance guard the operations guide
  asks for. Its integration tests are behind a build tag, so the default
  `go test ./...` stays offline.
- `conformance` and `cmd/conformance`: the acceptance criteria for phases 1 and
  2 as runnable checks against a live directory, one line per criterion, with a
  non-zero exit when one fails and `-json` for pasting into a ticket. The same
  cases run as Go subtests via `conformance.Run`, so a partner can gate their
  CI on them. The write cases are opt-in and built to leave the directory as
  they found it.
- `scim.Client.Page`, `scim.Client.Patch` and `ListResponse.Users`: the
  primitives the conformance suite needs to probe a single page, a deliberately
  forbidden attribute, and a decoded page. `ListUsers` and `PatchExternalID`
  are now thin policy wrappers over them.
- `store/storetest`: a contract test suite any `store.Store` implementation can
  run to prove it satisfies the rules the interface cannot express.
- `-dry-run`: reports what a cycle would do and writes nothing — not to the
  store, not to the directory, not to the checkpoint.
- A startup preflight that reads `ServiceProviderConfig` and refuses to run
  against a directory that reports no filter or no PATCH support.
- `/readyz`, distinct from `/healthz`: ready only after one cycle completes.
- `-cycle-timeout` (default 30m), so a hung cycle cannot block every later one.
- `docs/INTEGRATION.md`, `docs/OPERATIONS.md`, `docs/IMPLEMENTING_STORE.md`.

### Changed

- Wording: an account that stops being able to operate is a **closure**, not a
  "termination". The previous phrasing implied an employment relationship that
  this integration does not describe, and this repository is public. The
  machine sense is untouched — a page still terminates a walk.
- `deploy/schema.sql` is gone; `store/postgres/migrations/0001_init.sql` is the
  single source of truth for the schema. Two copies would have drifted.
- **`store.Helpper.ID` is now a `string`** (was `int64`), and `Helpper.HelpperID()`
  is gone — use `Helpper.ID`. A partner whose helpper identifier is a UUID could
  not implement the old interface honestly.
- **`Store.UpdateHelpper` takes a `store.HelpperUpdate`** instead of positional
  `enabled, displayName, login` arguments. Two adjacent strings were a silent
  swap waiting to happen.
- `Stats` distinguishes `Skipped` (inactive and unknown) from `Unchanged`, and
  counts `Malformed`; `Renamed` is now `Updated`.
- The package formerly named `sync` is `directory`, so it no longer shadows the
  standard library for callers.

### Fixed

- **The worker refuses to run against a real directory with an ephemeral
  store.** An empty store made every record look new, so a run with the default
  in-memory store would create fresh helppers and overwrite every `externalId` in
  the directory. This was reachable straight from the README's quickstart.
- **One unusable record no longer halts synchronization forever.** It used to
  fail the cycle, which froze the checkpoint, which re-read the same record next
  cycle — stopping the whole fleet. Unusable records are now skipped and
  alerted, the watermark is held behind the oldest of them so their eventual
  repair is not missed, and only a flood of them fails the cycle.
- **A permanent write-back conflict no longer alerts every five minutes.**
  Alerts are raised once per identity per process.
- **Pagination cannot loop forever.** A directory that ignores `startIndex`, or
  a proxy caching the first page, is detected and reported.
- **A non-JSON `200` is rejected instead of being read as an empty directory.**
  An HTML block page used to decode into zero records, which a full walk then
  reported as everyone having disappeared.
- Error bodies that are valid JSON but not SCIM keep their text, instead of
  being decoded into an empty error.
- A directory timestamp implausibly far in the future no longer advances the
  checkpoint past records that were never read.
- The default `User-Agent` names this library rather than a partner.
