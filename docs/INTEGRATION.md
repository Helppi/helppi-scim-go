# The integration contract

What the two sides promise each other, and where each promise is enforced in
this repository. Section numbers refer to the Helppi partner directory technical
proposal.

## Who does what

Helppi publishes a directory scoped to one partner. The partner reads it and
converges its own accounts. Nothing is pushed: there are no webhooks to miss and
no delivery order to preserve.

| Direction | Operation | Owner |
|---|---|---|
| Partner → Helppi | `GET /Users`, `GET /Users/{id}` | Partner reads |
| Partner → Helppi | `PATCH /Users/{id}` — `externalId` only | Partner writes exactly one attribute |
| Helppi → Partner | nothing | Helppi never calls the partner |

`POST`, `PUT` and `DELETE` are refused. The partner cannot create, rename or
remove a person; only Helppi can.

## The identity model (§05)

| Attribute | Meaning | Who owns it |
|---|---|---|
| `id` | Opaque directory identifier, stable for life | Helppi |
| `userName`, `emails[].value` | Alias at `@separador.app`, in e-mail shape | Helppi |
| `displayName`, `name` | Abbreviated name, e.g. `Marcio C.` | Helppi |
| `active` | Whether the person may work | Helppi |
| `externalId` | The partner's own identifier for this person | **Partner** |

The directory `id` is the only key the two sides match on. Never match by
`userName`, `displayName` or e-mail: aliases and names change, and matching on
them silently pairs the wrong people.

The directory carries no real e-mail address, no full legal name, no phone,
no document number and no Helppi-internal identifier. That is deliberate and is
the reason the alias exists (§05).

## Lifecycle (§06)

```
                 active:true, unknown to us
                            │
                            ▼
   ┌────────── create helpper, PATCH externalId ──────────┐
   │                                                     │
   ▼                                                     │
enabled ──── active:false ────► disabled ── active:true ─┘
   │                               │
   │                               └── still present in the directory,
   │                                   for the agreed retention window
   └── record eventually disappears after retention: DO NOTHING
```

Three rules follow, and this client enforces all three:

1. **A person who is inactive and unknown is never created.** Creating a
   disabled account is worse than not having it: it looks like access.
2. **Reactivation reuses the same helpper.** The directory `id` is the key, so a
   person who leaves and returns is the same account, not a second one.
3. **Absence never deprovisions.** A termination always arrives explicitly as
   `active: false` while the record is still visible. A record that vanishes is
   a record whose retention window ended — the disable arrived long before. If
   an enabled helpper is missing from a full walk, that is *drift* to report, not
   an instruction to act on.

## The write-back (§07)

After creating the helpper, write your identifier back to the directory record:

```http
PATCH /Users/hlp_8fK2Lm91
Content-Type: application/scim+json

{
  "schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
  "Operations": [{ "op": "replace", "path": "externalId", "value": "782193" }]
}
```

Two consequences worth knowing in advance:

- **The write bumps `meta.lastModified`**, so the record comes back on your next
  incremental query. That is expected. Applying it again must be a no-op — which
  is why every operation here is idempotent.
- **A `409` means your mapping is wrong**, not that the request was malformed.
  The value is already bound to a different identity, usually a duplicate
  account created before a unique index existed. Retrying cannot fix it.

## Error matrix

| Status | Meaning | What the client does | What you should do |
|---|---|---|---|
| `400` | Malformed request | Fails the cycle | Fix the caller; it is a bug |
| `401` | Missing or revoked credential | Fails immediately, no retry | Page someone; rotate or restore the token |
| `403` | Outside what this credential may do | Fails immediately, no retry | You attempted a write other than `externalId` |
| `404` | Unknown id, or outside your scope | Typed error | Do not recreate; the person may have left your scope |
| `409` | `externalId` already bound elsewhere | Alerts once, cycle continues | Human investigation: you likely have a duplicate |
| `429` | Rate limited | Honors `Retry-After`, retries | Lower `-rps` if it persists |
| `5xx` | Directory fault | Retries with backoff | Nothing; the next cycle recovers |

Anything that is not JSON, and any `200` that is not a `ListResponse`, is
rejected rather than interpreted. A proxy error page must never be read as
"the directory is empty".

## Synchronization policy (§08)

| Decision | This client | Why |
|---|---|---|
| Cadence | 5 min incremental, 24 h full | Access removal within minutes; the daily walk is the net |
| Incremental query | `meta.lastModified gt <checkpoint − 2 min>` | The overlap absorbs commit-visibility races |
| Checkpoint source | Directory timestamps, never the local clock | Removes clock skew between two companies |
| Checkpoint advance | Only after a cycle completes in full | A partial advance loses records silently |
| Unusable record | Skipped and alerted; the watermark is held behind it | Halting the fleet over one bad record is worse; holding the watermark means its fix is not missed |
| Too many unusable records | Cycle fails (default: more than 25) | Skipping a few is prudence, skipping hundreds hides an outage |
| Future timestamps | Rejected as a watermark, record still applied | A fast clock upstream would skip everything in between |
| Permanent conflict | Alerted once per identity per process | Otherwise it pages someone every five minutes forever |

## Where each promise lives

| Promise | Code | Test |
|---|---|---|
| Only `externalId` is ever written | `scim.Client.PatchExternalID` | `TestPatchExternalIDSendsTheContractualBody` |
| Matching is by directory `id` | `directory.Syncer.apply` | `TestSuspensionThenReactivation` |
| Inactive and unknown is not created | `directory.Syncer.apply` | `TestFirstCycleCreatesActiveHelppersAndWritesBack` |
| Absence never deprovisions | `directory.Syncer.Full` | `TestFullReportsDriftWithoutDeprovisioning` |
| A missing `active` is never "disabled" | `directory.validate` | `TestMalformedRecordIsSkippedNotFatal` |
| The checkpoint holds on failure | `directory.Syncer.advance` | `TestFailedCycleIsRetriedFromTheSameCheckpoint` |
| The echo of our own write is a no-op | idempotent `apply` | `TestPartnerWriteBackEchoIsHarmless` |
| One identity, one helpper, under races | unique index + `ensureHelpper` | `TestCreateRaceFallsBackToTheExistingHelpper` |
| A proxy page is not an empty directory | `scim.Client.attempt` | `TestRejectsAnHTMLResponseInsteadOfReadingItAsAnEmptyDirectory` |

## What the fake directory does not do

`scimtest` is good enough to develop against, and deliberately not a simulator.
It understands only `meta.lastModified gt "<RFC3339>"` — no `and`, no `ge`, no
attribute filters — and implements `startIndex`/`count` pagination only, with no
cursor support. Passing against it is necessary, not sufficient: run the same scenarios against
the Helppi sandbox before calling Phase 1 done.
