# helppi-scim-go

[![CI](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml/badge.svg)](https://github.com/Helppi/helppi-scim-go/actions/workflows/ci.yml)

**A working client for the Helppi partner directory** — the code that keeps your
records of Helppi professionals in step with ours, automatically.

*Versão em português: [README.pt-BR.md](README.pt-BR.md).*

> **How to read this.** Part 1 is written for product and operations: what this
> is, what changes when it runs, and what your team has to do. Part 2 is the
> engineering reference. You do not need Part 2 to decide whether this is worth
> doing.

---

# Part 1 · What this is

## In one paragraph

Helppi publishes a directory of the professionals authorized to work with you.
This repository is a **reference client for that directory**: a small program
that asks Helppi "who exists, and who is allowed to work right now?", applies
the answer to your own accounts, and hands back the identifier you generated so
both sides share a permanent key. It is written in Go, it has no dependencies,
and it comes with a test suite that proves your integration works before it ever
touches production.

It accompanies the *Helppi partner directory technical proposal*. The proposal
describes the agreement; this repository is the agreement, executable.

## The problem it solves

Today a professional's record reaches you once, when they join, and then the two
sides drift apart. When someone is suspended or has their registration closed at
Helppi, nothing tells you. Their account on your side stays open until a person
notices and asks someone to fix it — by message, by spreadsheet, always after
the fact.

That gap is a security problem before it is an operational one: access that
outlives the reason for it.

## What changes when this is running

| Today | With the directory |
|---|---|
| The record arrives once, on the way in | The state is consulted every few minutes |
| A suspension never reaches you | The account is blocked within the agreed interval |
| No shared key between the two sides | A stable pair of identifiers, set once |
| Someone notices, then asks for a fix | Nobody intervenes; it converges on its own |
| "Which account is this person?" is manual work | The answer is a lookup |

## How it works, in plain language

Every few minutes the client asks Helppi a single question: *what changed since
the last time I asked?* Helppi answers with the professionals whose state moved
— someone joined, someone was suspended, someone came back, someone's
registration was closed. The client applies each answer to your accounts:
creates, blocks, unblocks.

The first time it sees a professional, it creates the account on your side and
then **writes your identifier back into Helppi's record**. That single step is
what creates the shared key. From then on, both companies point at the same
person with the same pair of identifiers, and nobody ever has to match by name
or e-mail again — which matters, because names and e-mails change and are
exactly the data neither company should be moving around.

Once a day it does a full pass, comparing everything against everything, and
reports any disagreement. It reports; it does not act. A professional missing
from the directory is never treated as an instruction to delete anything.

Three properties are worth knowing, because they are what make this safe to run
unattended:

- **It cannot lose an update.** If a cycle fails halfway, it does not record
  progress, so the next cycle simply does the same work again.
- **Repeating is free.** Applying the same answer twice changes nothing, so
  retrying is always safe.
- **It never guesses.** A record that arrives incomplete is skipped and reported
  rather than interpreted — because interpreting "I don't know" as "blocked"
  would take everybody's access away at once.

## What your team has to do

Three things. Everything else is in this repository.

**1 · Connect it to your database.** You implement one interface — six methods:
find, create, update, list, and read/write a timestamp. That is the only code
this integration genuinely requires you to write. If you run PostgreSQL, even
this is already done: [`store/postgres`](store/postgres) is a working
implementation you can use as-is.

**2 · Run one process.** A worker is included, with metrics and health checks.
It runs continuously, or once per scheduled run — whichever fits how you deploy.

**3 · Return your identifier.** When you create an account, write its id back to
Helppi's record. One call, once per professional.

Then run the conformance command against Helppi's test environment. It prints a
pass/fail line per requirement, and that report **is** the acceptance criterion
for Phase 1 — not an opinion, not a meeting.

## What you get without writing it

| | |
|---|---|
| **A fake Helppi directory** | Develop and test offline, with no sandbox and no credentials. It also simulates the failures — rate limits, conflicts, malformed records — so you can see how your side behaves before it matters. |
| **A conformance command** | 14 checks across both phases, each naming the requirement it defends. Exits non-zero on failure, so it works as a gate in your pipeline. |
| **A contract test suite** | Point it at your database implementation and it verifies the rules the method signatures cannot express — including that eight simultaneous workers create exactly one account, not eight. |
| **A ready worker** | Metrics, health checks, structured logs, a dry-run mode that writes nothing, and a guard that refuses to run in a configuration that could corrupt data. |
| **A PostgreSQL store** | In its own module, so nobody who doesn't want it pays for the dependency. |

## What this does not do

- **It does not log anyone in.** Single sign-on is a separate concern, described
  in the proposal. This client is only about who exists and who is allowed.
- **It does not receive personal data beyond the minimum.** No real e-mail, no
  full legal name, no phone or document number. What crosses is an opaque
  identifier, an alias, an abbreviated name, and a status.
- **It does not delete anything on its own.** Absence from the directory is
  reported as a discrepancy, never acted on.
- **It does not write to Helppi**, except for that one identifier.

## Questions that usually come up

**Do we have to use Go?**
No. If your stack is something else, this repository is still useful as the
executable specification: the behaviour is documented, the failure cases are
named, and the fixtures in `testdata/` are the same bytes both sides can test
against. The conformance command runs against any implementation, in any
language, because it only speaks HTTP.

**What if a sync fails?**
Nothing breaks. The client did not record progress, so the next cycle repeats
the work. A cycle that fails is a delayed cycle, not a lost one.

**How fast does a block take effect?**
Whatever interval the two companies agree on. The proposal suggests five
minutes; the number is a setting, not a rewrite.

**What if we already have accounts for these people?**
The first full pass finds them by directory identifier and adopts them. It does
not create duplicates — and there is a test that proves it.

**How do we know we are done?**
Run `conformance` against Helppi's test environment. Fourteen checks, each
mapped to a section of the proposal. When they all pass, Phase 1 is complete.

---

# Part 2 · Engineering reference

## Quickstart

```bash
go test ./... -race        # 45 tests, no network
make ci                    # gofmt + vet + tests
```

```go
client, err := scim.New(scim.Options{BaseURL: url, Token: token})
if err != nil {
    return err
}

syncer := directory.New(client, myStore, directory.Options{})

stats, err := syncer.Incremental(ctx)   // one cycle
```

Then prove your store satisfies the contract:

```go
func TestMyStore(t *testing.T) {
    storetest.Run(t, func(t *testing.T) store.Store { return newTestStore(t) })
}
```

## Start with a dry run

```bash
DIRECTORY_BASE_URL=… DIRECTORY_TOKEN=… make dry-run
```

Writes nothing — not to your store, not to the directory, not to the checkpoint.

The worker **refuses** to run for real against a directory with the in-memory
store. An empty store makes every record look new, so it would create fresh
accounts and overwrite every `externalId` in the directory. Plug in a real
`store.Store` first.

## The model: a reconciler, not an event consumer

Every cycle re-derives the desired state from the directory and converges. There
is no ordering to preserve and no event to lose, so the worker can be killed and
restarted at any point.

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
| `store` | The local-side contract, an in-memory implementation, and a contract test suite. |
| `store/postgres` | A PostgreSQL implementation, in its own module so the core stays dependency-free. |
| `directory` | The reconciler. Knows nothing about HTTP. |
| `scimtest` | Fake directory with fault injection. |
| `conformance` | The acceptance criteria as runnable checks. |
| `cmd/directorysyncd` | The worker: two tickers, structured logs, `/metrics`, `/healthz`, `/readyz`. |
| `cmd/conformance` | Runs the checks against a live directory and prints a report. |

## The store contract

```go
type Store interface {
    HelpperByDirectoryID(ctx context.Context, directoryID string) (Helpper, error)
    CreateHelpper(ctx context.Context, p NewHelpper) (Helpper, error)
    UpdateHelpper(ctx context.Context, id string, upd HelpperUpdate) error
    EnabledHelppers(ctx context.Context) ([]Helpper, error)

    Checkpoint(ctx context.Context) (time.Time, error)
    SetCheckpoint(ctx context.Context, at time.Time) error
}
```

`Helpper.ID` is a string, so a UUID, a ULID or a bigint all fit. See
[docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) — the two load-bearing
lines of the schema are in there, and neither of them is Go.

## Nine decisions worth arguing about

1. **`Active` is a `*bool`, not a `bool`.** With a plain `bool`, a truncated
   response or a missing attribute decodes to `false` — and the reconciler
   disables the entire fleet. `nil` is refused, never read as "disabled".
2. **The checkpoint advances only when the cycle completes.** A partial cycle
   that advanced the watermark loses records permanently and silently; the
   symptom shows up weeks later as one account nobody blocked.
3. **The watermark comes from `meta.lastModified`, not the local clock.** Clock
   skew between two companies stops mattering. A timestamp implausibly far in
   the future is refused rather than trusted.
4. **Two minutes of overlap** are re-read every cycle to absorb
   commit-visibility races. Safe because applying a record is idempotent.
5. **An unusable record is skipped, not fatal — but the watermark is held
   behind it.** Failing the cycle would freeze the checkpoint and stop the whole
   fleet over one bad row; skipping without holding the watermark would lose its
   eventual fix.
6. **Matching is by directory `id` only.** Never by login, name or alias, at any
   stage, including the initial load.
7. **A `409` on write-back is never retried, and alerts once per identity.** It
   means the local mapping is wrong; retrying cannot fix it, and re-alerting
   every five minutes trains people to ignore the alert.
8. **Absence from the directory never deprovisions.** The daily walk reports the
   drift and stops there.
9. **A response that is not SCIM is an error, not an empty directory.** An HTML
   block page decoded loosely becomes "nobody works here any more".

## Conformance

```bash
go run ./cmd/conformance -base-url … -token …
```

Fourteen checks — eleven for Phase 1, three for Phase 2 — each naming the
requirement it defends. `--json` for machine output. See
[docs/CONFORMANCE.md](docs/CONFORMANCE.md).

## Documentation

| Document | What it answers |
|---|---|
| [docs/INTEGRATION.md](docs/INTEGRATION.md) | The contract: identity model, lifecycle, error matrix, and which test defends each promise. |
| [docs/IMPLEMENTING_STORE.md](docs/IMPLEMENTING_STORE.md) | How to write the one interface you own. |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Metrics, alert thresholds, and a runbook per failure. |
| [docs/CONFORMANCE.md](docs/CONFORMANCE.md) | Every check, and the requirement behind it. |

## Requirements

Go 1.22 or newer for the core packages, which have **no third-party
dependencies** — vendor them and build offline if that is easier. The optional
`store/postgres` module needs Go 1.24, because pgx's dependency chain does.

## Out of scope

One-tap browser access is not here. It is an ordinary OpenID Connect client
whose callback maps the directory identifier to a session. See the proposal.

## License

MIT. See [LICENSE](LICENSE).
