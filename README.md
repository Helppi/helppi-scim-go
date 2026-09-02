# helppi-scim-go

[![CI](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml)

Reference client, in Go, for the Helppi partner directory — the SCIM 2.0
integration described in sections 06 to 08 and Appendix A of the Helppi
partner directory technical proposal.

*Versão em português: [README.pt-BR.md](README.pt-BR.md).*

This is not an SDK and it does not have to be adopted as-is. It is code that
compiles, runs and is tested, written so that two engineering teams can discuss
behavior against something concrete instead of against prose — and so that the
partner starts from a working reconciler rather than from a specification.

- **No dependencies.** Standard library only, Go 1.22+. Vendor the packages and
  build them offline if that is easier.
- **Tested against a fake directory** that implements the same contract,
  including the failures that matter: `403`, `409`, `429`, `5xx`, malformed
  records, broken pagination, and responses that are not SCIM at all.
- **A conformance runner.** One command checks a live directory against every
  acceptance criterion for phases 1 and 2, and exits non-zero if one fails, so
  "phase 1 is done" is an output rather than an opinion.

```bash
go test ./... -race        # 45 tests, no network
make ci                    # gofmt + vet + tests
```

## Start with a dry run

```bash
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make dry-run
```

A dry run reports what a cycle would do and writes nothing — not to your store,
not to the directory, not even to the checkpoint.

The worker **refuses** to run for real against a directory with the in-memory
store, and that guard is deliberate: an empty store makes every record look new,
so it would create fresh helppers and `PATCH` their invented ids over the real
`externalId` values. That corrupts data on Helppi's side. Plug in a real
`store.Store` first.

## Is the directory behaving?

```bash
DIRECTORY_TOKEN=… go run ./cmd/conformance \
    -base-url https://…/scim/v2 -alias-domain separador.app
```

```
PHASE 1 — Directory synchronization
  PASS  P1.01   Credential is accepted                          §13 configuration
  PASS  P1.04   active is present on every record               §06 lifecycle
  PASS  P1.07   startIndex and count are honored                Appendix A — pagination
  PASS  P1.08   Filtering by meta.lastModified works            §08 incremental sync
  ...
14 passed, 0 failed, 0 skipped
```

Read-only unless you name a record to write to. See
[docs/CONFORMANCE.md](docs/CONFORMANCE.md); the same cases run as Go subtests
via `conformance.Run(t, client, opts)` so they can gate your CI.

## Quickstart

The only thing you have to write is an implementation of `store.Store`.

```go
client, err := scim.New(scim.Options{BaseURL: url, Token: token})
if err != nil {
    return err
}

syncer := directory.New(client, myStore, directory.Options{})

// One cycle. Call it from wherever you schedule work.
stats, err := syncer.Incremental(ctx)
```

Then prove your store satisfies the contract — including the rules its method
signatures cannot express:

```go
func TestMyStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store { return newTestStore(t) })
}
```

## The model: a reconciler, not an event consumer

Every cycle re-derives the desired state from the directory and converges.
There is no ordering to preserve and no event to lose, so the worker can be
killed and restarted at any point.

```
every 5 min ──► incremental cycle ──► apply(record) ──► write back externalId
                       │                                        │
             checkpoint advances only if                  409 ⇒ alert once,
             the cycle completed in full                  never retried
every 24 h ─► full walk ──► drift report
```

## Layout

| Package | Responsibility |
|---|---|
| `scim` | Protocol: types, HTTP client, pagination, retry. Knows nothing about helppers. |
| `store` | The local-side contract, plus an in-memory implementation and a contract test suite. |
| `store/postgres` | A PostgreSQL implementation, in its own module so the core stays dependency-free. |
| `directory` | The reconciler. Knows nothing about HTTP. |
| `conformance` | The acceptance criteria, as runnable checks. |
| `scimtest` | Fake directory with fault injection. |
| `cmd/directorysyncd` | The worker: two tickers, structured logs, `/metrics`, `/healthz`, `/readyz`. |
| `cmd/conformance` | Checks a live directory and prints one line per criterion. |

## Documentation

| Document | What it answers |
|---|---|
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | The contract: identity model, lifecycle, error matrix, and which test defends each promise. |
| [docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) | How to write the one interface you own, and the two load-bearing lines of the schema. |
| [docs/CONFORMANCE.md](docs/CONFORMANCE.md) | What each acceptance case checks, and why the write cases are opt-in. |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Metrics, alert thresholds and a runbook per failure. |

## Nine decisions worth arguing about

1. **`Active` is a `*bool`, not a `bool`.** With a plain `bool`, a truncated
   response or a missing attribute decodes to `false` — and the reconciler
   disables the entire fleet. `nil` is refused, never read as "disabled".
2. **The checkpoint advances only when the cycle completes.** A partial cycle
   that advanced the watermark loses records permanently and silently; the
   symptom shows up weeks later as one helpper nobody blocked.
3. **The watermark comes from `meta.lastModified`, not the local clock.** Clock
   skew between two companies stops mattering. A timestamp implausibly far in
   the future is refused rather than trusted.
4. **Two minutes of overlap** are re-read every cycle to absorb
   commit-visibility races. Safe because applying a record is idempotent —
   which is the whole reason to insist on idempotency.
5. **An unusable record is skipped, not fatal — but the watermark is held
   behind it.** Failing the cycle would freeze the checkpoint and stop the whole
   fleet over one bad row; skipping without holding the watermark would lose its
   eventual fix. Only a flood of them fails the cycle.
6. **Matching is by directory `id` only.** Never by login, name or alias, at any
   stage, including the initial load.
7. **A `409` on write-back is never retried, and alerts once per identity.** It
   means the local mapping is wrong; retrying cannot fix it, and re-alerting
   every five minutes trains people to ignore the alert.
8. **Absence from the directory never deprovisions.** The daily walk *reports*
   the drift and stops there: a closure always arrives explicitly, as
   `active: false`, before the record disappears.
9. **A response that is not SCIM is an error, not an empty directory.** An HTML
   block page decoded loosely becomes "nobody works here any more".

## What to replace before production

- `store/memory` → your own `store.Store`, verified with `storetest.Run` — or
  just use [`store/postgres`](store/postgres), which already passes it. The
  unique index on `directory_id` is what guarantees idempotent creation — the
  check in Go is a fast path, not a guarantee.
- `Options.Alert` → your on-call channel.
- `cmd/directorysyncd/metrics.go` → your metrics registry.
  `directory_sync_lag_seconds` is the one that matters: it is the SLI behind
  "an account closure reaches the partner within N minutes", and it catches a stuck
  worker even when nothing is erroring.
- **Run exactly one instance.** Two replicas on the same schedule will both try
  to create helppers on the first sync. The unique index prevents duplicates, but
  the `409` storm is avoidable: use a lease, a Postgres advisory lock, or
  `concurrencyPolicy: Forbid`.

## Out of scope

One-tap browser access (sections 09 to 11 of the proposal) is not here. It is an
ordinary OpenID Connect client — `golang.org/x/oauth2` plus
`github.com/coreos/go-oidc` — whose callback does
`sub → helppers.directory_id → session`. The alternative path, with a signed
launch URL, is a JWT verification plus a single-use record.

## License

MIT. See [LICENSE](LICENSE).
