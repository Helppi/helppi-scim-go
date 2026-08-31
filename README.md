# helppi-scim-go

Reference client, in Go, for the Helppi partner directory — the SCIM 2.0
integration described in sections 06 to 08 and Appendix A of the
*Helppi partner directory technical proposal*.

*Versão em português: [README.pt-BR.md](README.pt-BR.md).*

This is not an SDK and it does not have to be adopted as-is. It is code that
compiles, runs and is tested, written so that two engineering teams can discuss
behaviour against something concrete instead of against prose — and so that the
partner starts from a working reconciler rather than from a specification.

- **No dependencies.** Standard library only, Go 1.22+. Copy the packages into
  your own repository and build them offline if that is easier.
- **Tested against a fake directory** that implements the same contract,
  including the errors the integration defines (`403`, `409`, `429`, `5xx`).
- **`testdata/directory.json` is the conformance set**: the five lifecycle
  states from the proposal plus the `picker_id` write-back cases. Both sides can
  test against the same bytes.

```bash
go test ./... -race        # 21 tests, ~2s, no network
make build                 # bin/directorysyncd
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make run-once
```

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

## The model: a reconciler, not an event consumer

Every cycle re-derives the desired state from the directory and converges.
There is no ordering to preserve and no event to lose, so the worker can be
killed and restarted at any point.

```
every 5 min ──► incremental cycle ──► apply(record) ──► write back picker_id
                       │                                        │
             checkpoint advances only if                  409 ⇒ alert,
             the cycle completed in full                  never retried
every 24 h ─► full walk ──► drift report
```

## Layout

| Package | Responsibility |
|---|---|
| `scim` | Protocol: types, HTTP client, pagination, retry. Knows nothing about pickers. |
| `store` | The local-side contract (pickers and checkpoint) plus an in-memory implementation. |
| `directory` | The reconciler. Knows nothing about HTTP. |
| `scimtest` | Fake directory with fault injection. |
| `cmd/directorysyncd` | The worker: two tickers, structured logs, `/metrics` and `/healthz`. |
| `deploy/schema.sql` | Reference Postgres schema. |

That separation is what lets you test the reconciler against a fake directory
and the client against fixed responses, without standing anything up.

## Seven decisions worth arguing about

1. **`Active` is a `*bool`, not a `bool`.** With a plain `bool`, a truncated
   response or a missing attribute decodes to `false` — and the reconciler
   disables the entire fleet. `nil` is an error, never "disabled".
2. **The checkpoint advances only when the cycle completes.** A partial cycle
   that advanced the watermark loses records permanently and silently; the
   symptom shows up weeks later as one picker nobody blocked.
3. **The watermark comes from `meta.lastModified`, not the local clock.** Clock
   skew between two companies stops mattering.
4. **Two minutes of overlap** are re-read every cycle to absorb
   commit-visibility races on the directory side. Safe because `apply` is
   idempotent — which is the whole reason to insist on idempotency.
5. **Matching is by directory `id` only.** Never by login, name or alias, at any
   stage, including the initial load.
6. **A `409` on write-back is never retried.** It means the local mapping is
   wrong; retrying cannot fix it and a retry loop hides it. It raises an alert.
7. **Absence from the directory never deprovisions.** The daily walk *reports*
   the drift and stops there: a termination always arrives explicitly, as
   `active: false`, before the record disappears.

## What to replace before production

- `store/memory` → your own `store.Store`. The contract is in `store/store.go`
  and the reference schema in `deploy/schema.sql`. The unique index on
  `directory_id` is what guarantees idempotent creation — the check in Go is a
  fast path, not a guarantee.
- `Options.Alert` → your on-call channel.
- `cmd/directorysyncd/metrics.go` → your metrics registry.
  `directory_sync_lag_seconds` is the one that matters: it is the SLI behind
  "a termination reaches the partner within N minutes", and it alerts on a stuck
  worker even when nothing is erroring.
- **Run exactly one instance.** Two replicas on the same schedule will both try
  to create pickers on the first sync. The unique index prevents duplicates, but
  the resulting `409` storm is avoidable: use a lease, a Postgres advisory lock,
  or `concurrencyPolicy: Forbid`.

## Test coverage

| Scenario | Test |
|---|---|
| Pagination through the last page | `TestListUsersWalksEveryPage` |
| Incremental query by timestamp | `TestListUsersHonoursFilter` |
| A missing `active` attribute decodes to `nil` | `TestActiveIsNilWhenAbsent` |
| `429` honours `Retry-After`; `5xx` backs off | `TestRetriesOn429AndHonoursRetryAfter` |
| `401` is not retried | `TestCredentialErrorIsNotRetried` |
| `PATCH` body matches the contract | `TestPatchExternalIDSendsTheContractualBody` |
| First cycle creates and writes back the `picker_id` | `TestFirstCycleCreatesActivePickersAndWritesBack` |
| Second cycle is a no-op | `TestSecondCycleIsANoOp` |
| Suspension and reactivation reuse the same picker | `TestSuspensionThenReactivation` |
| The checkpoint comes from the directory | `TestCheckpointComesFromDirectoryTimestamps` |
| A malformed record aborts the cycle and holds the checkpoint | `TestMissingActiveFlagAbortsCycleAndHoldsCheckpoint` |
| A failed cycle replays from the same point | `TestFailedCycleIsRetriedFromTheSameCheckpoint` |
| Our own write-back echo is harmless | `TestPartnerWriteBackEchoIsHarmless` |
| Drift is reported, not acted on | `TestFullReportsDriftWithoutDeprovisioning` |
| The full walk catches what the incremental path missed | `TestFullDetectsAPickerTheIncrementalPathMissed` |
| A write-back conflict alerts without failing the cycle | `TestWriteBackConflictAlertsAndDoesNotFailTheCycle` |
| A creation race does not duplicate a picker | `TestCreateRaceFallsBackToTheExistingPicker` |

These scenarios are the acceptance criteria for Phases 1 and 2 of the proposal.
A client that passes them against the Helppi test environment has completed
Phase 1.

## Out of scope

One-tap browser access (sections 09 to 11 of the proposal) is not here. It is an
ordinary OpenID Connect client — `golang.org/x/oauth2` plus
`github.com/coreos/go-oidc` — whose callback does
`sub → pickers.directory_id → session`. The alternative path, with a signed
launch URL, is a JWT verification plus a single-use record.

## License

MIT. See [LICENSE](LICENSE).
