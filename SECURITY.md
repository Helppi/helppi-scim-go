# Security

## Reporting a vulnerability

Report suspected vulnerabilities privately to **security@helppi.io**. Please do
not open a public issue.

Include what you can: affected version or commit, what an attacker achieves, and
a reproduction. We acknowledge reports within three business days and will tell
you our assessment and expected timeline.

## Scope

This repository is a client library and a reference worker. It holds no
credentials and no data.

In scope: anything in this repository — the SCIM client, the reconciler, the
fake directory, the worker.

Out of scope: the Helppi directory service itself and its credential issuance.
Report those through the same address; they follow a different process.

## What this code assumes

- **The bearer token is a secret.** It is read from `DIRECTORY_TOKEN` and sent
  only to the configured base URL. It is never logged. Supply it through your
  secret manager, not a command line.
- **TLS is verified.** The client uses Go's default `http.Client`; it does not
  disable verification and offers no option to.
- **The directory is trusted to be honest, not to be correct.** Malformed
  records are refused rather than guessed at — see the `active` handling in
  `docs/INTEGRATION.md` — but the client does not defend against a directory
  that lies about who is active.
- **Directory content is personal data.** Aliases and abbreviated names are
  still information about real people. Do not copy them into logs, tickets or
  test fixtures.

## Support

Bug fixes in this reference client. It carries no operational commitment to any
partner's deployment.
