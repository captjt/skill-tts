# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through [GitHub security advisories](https://github.com/captjt/skill-tts/security/advisories/new), not in public issues. You should get a response within a few days.

## What this project defends against

**Tampered binaries.** The launcher runs a release binary only if its SHA-256 matches the hash pinned in the launcher source. Release builds are reproducible, so the pins can be checked independently with `make verify-pins`. The release workflow:

- refuses to publish binaries that don't match the pins,
- attaches build provenance attestations,
- pins every GitHub Action to a full commit SHA.

**Leaking API keys.**

- Keys are read only from environment variables or the OS keyring.
- They are never written to disk by tts, never logged, and never passed in argv, where other local processes could read them.
- HTTP redirects are not followed, so a key can't be forwarded to another host.
- Provider error messages are relayed without the request headers.

**Leaking spoken text.**

- The default provider is offline.
- Text goes only to the provider you select.
- Text reaches subprocesses and the background worker over stdin pipes, not argv or temp files.

**Signalling the wrong process.** `tts stop` only signals processes that hold a live lock on their own lock file. A stale PID that has been reused by an unrelated process is never signalled.

**Unexpected spend.** Paid providers refuse text longer than 20,000 characters by default.

## Out of scope

- A compromised user account. Anyone who can write to your home directory can already replace the cached binary or the skill itself.
- The privacy practices of the cloud providers you choose to use.
