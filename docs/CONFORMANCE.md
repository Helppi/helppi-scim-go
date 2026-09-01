# Proving the integration works

The acceptance criteria for phases 1 and 2 of the rollout are checked by a
command, so "we finished phase 1" is an output rather than an opinion.

```bash
DIRECTORY_TOKEN=… go run ./cmd/conformance \
    -base-url https://…/scim/v2 \
    -alias-domain separador.app
```

```
PHASE 1 — Directory synchronization
  PASS  P1.01   Credential is accepted                          §13 configuration
  PASS  P1.02   Responses are SCIM ListResponses                Appendix A — envelope
  PASS  P1.03   Every record carries a stable id                §05 identity model
  PASS  P1.04   active is present on every record               §06 lifecycle
  ...

14 passed, 0 failed, 0 skipped  (2.6s)
```

It exits `0` when nothing failed, `1` when a case failed, and `2` when it could
not run at all — so it works as a pipeline gate. `-json` emits the same report
for pasting into an integration ticket.

Both sides get use out of it. Helppi runs it against its own directory to catch
a regression before a partner does; the partner runs it against the sandbox on
day one, and the answer to "is the endpoint behaving?" stops being a thread of
messages.

## Inside your own tests

The same cases run as Go subtests, so they can gate your CI rather than being a
thing someone remembers to do:

```go
func TestDirectoryConformance(t *testing.T) {
    client, err := scim.New(scim.Options{BaseURL: url, Token: token})
    if err != nil {
        t.Fatal(err)
    }
    conformance.Run(t, client, conformance.Options{AliasDomain: "separador.app"})
}
```

## What each case defends

| Case | Checks | Why it matters |
|---|---|---|
| P1.01 | The credential is accepted | Everything else is meaningless without it, so a failure here skips the rest with a reason instead of reporting fourteen failures for one problem |
| P1.02 | Responses are SCIM `ListResponse`s, `startIndex` is 1-based | A proxy page decoded loosely reads as an empty directory |
| P1.03 | Every record has a stable, unique `id` | It is the only key the two sides match on |
| P1.04 | `active` is present on **every** record | The one attribute a client must never guess: read as "disabled" it blocks the fleet, read as "enabled" it leaves terminated people working |
| P1.05 | Identities are aliases on the agreed domain | The data-minimization promise, checked rather than trusted |
| P1.06 | `meta.lastModified` is present and not in the future | It is the watermark; a fast clock upstream would skip everything in between |
| P1.07 | `startIndex` and `count` are honored | A directory that ignores `startIndex` makes a walking client loop forever |
| P1.08 | Filtering by `meta.lastModified` narrows the set | If the filter is ignored, incremental sync silently becomes a full walk every five minutes |
| P1.09 | A record can be read by id, and matches the listing | |
| P1.10 | An unknown id is a typed `404` | Clients branch on it; an HTML 404 is not enough |
| P1.11 | `ServiceProviderConfig` advertises filter and patch | Better discovered at startup than at 03:00 |
| P2.01 | `externalId` accepts your identifier and reflects it | |
| P2.02 | Writing the same value twice changes nothing | Your own write bumps `meta.lastModified`, so the record returns next cycle and is applied again |
| P2.03 | Authoritative attributes are refused | Without this, a partner can silently re-enable someone |

## The write cases are opt-in, and non-mutating

Phase 2 is skipped unless `-write-id` names a record reserved for probing.
Writing to an arbitrary record in someone else's directory is not ours to do.

Even with a target, the cases are built to leave the directory as they found
it:

- **P2.01 and P2.02** rewrite the value the record *already carries*, so the
  write path is proven without changing anything. If the record has no
  `externalId` yet, the case is skipped unless you supply `-probe-external-id`.
- **P2.03** patches `active` to the value it already holds. If the directory
  correctly refuses, we learn that. If it wrongly accepts, nobody's access
  changed — which is the only responsible way to probe a refusal.

## Skips are not failures

A skipped case is one the operator did not enable, or one the directory's shape
made impossible — fewer than two records, no timestamps to filter on, no
`ServiceProviderConfig` endpoint. Skips never fail the run. Read them: a run
that is mostly skips has proven very little.
