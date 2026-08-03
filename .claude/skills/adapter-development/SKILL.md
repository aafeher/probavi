---
name: adapter-development
description: Rules and workflow for creating or modifying Probavi engine adapters and anything touching the adapter protocol. Use this skill whenever the task mentions adapters, a database engine integration (postgres, mysql, mongo, pgbackrest, wal-g, mysqldump), the files under adapters/ or internal/adapter/, docs/adapter-protocol.md, or the provision/probe/healthcheck/teardown operations — even if the word "adapter" is not used.
---

# Probavi adapter development

## Before writing any code

1. Read `docs/adapter-protocol.md` in full. It is normative. If the task requires something the protocol cannot express, STOP: propose a protocol change (doc PR with version bump) and ask the maintainer — never invent undocumented fields or side channels.
2. Read one existing adapter end-to-end (start with `adapters/postgres/`) to copy its structure, error mapping, and test layout.

## Hard rules

- Adapters are external processes speaking line-delimited JSON on stdin/stdout as defined in `docs/adapter-protocol.md`: one request in, core-mediated sandbox-verb calls both ways (`exec`, `put_file`; at most one outstanding), exactly one final response out. Logs on stderr only. Never print anything non-protocol to stdout — it corrupts the stream and the core fails the operation as `adapter_crash`.
- Engine-specific logic lives ONLY in the adapter. If the core (`internal/`) needs to learn an engine concept for your change to work, the design is wrong — bring it back to the protocol level.
- `teardown` must be idempotent and must succeed on partially-provisioned state. Test the crash-mid-provision path explicitly.
- Handle SIGTERM: begin cleanup immediately; the core will SIGKILL after the grace period.
- Credentials arrive via environment variables defined in the protocol doc; never accept or emit secrets inside JSON payloads, and never log them.
- Every response includes accurate timing data — restore duration feeds the evidence record and the product's RTO trend feature; do not estimate, measure.

## Workflow

1. Implement `probe` first and make `probavi adapter probe` show correct capabilities.
2. Implement `provision` against the simplest source kind; get one manual end-to-end drill passing.
3. Add `healthcheck` + `teardown`, then the failure paths (missing source, corrupt backup, sandbox death mid-restore).
4. Run the conformance suite (`probavi adapter conformance <cmd>`) — a new adapter is done only when conformance passes. *(The suite is a Phase 2 deliverable and does not exist yet; until it lands, the protocol doc's MUSTs and the postgres adapter's test suite are the reference.)*
5. Golden-file tests for every request/response pair you implement; table-driven tests for source discovery logic.

## Definition of done

Conformance passes (once the Phase 2 suite exists — until then: every protocol MUST covered by tests); no stdout pollution; teardown-after-crash test exists; timings are real measurements; README for the adapter documents supported source kinds and env vars.

Plus the published-capability half:

- `adapters/<id>/adapter.json` carries a display name for **every** source kind the probe declares (the generator fails on any mismatch, in either direction), the engine versions and images CI restores from, the maturity value, and `conformance_verified` — which makes the conformance suite drive this adapter, so the claim cannot be aspirational.
- Re-run `go generate ./...` so `docs/capabilities.json` reflects the adapter, and commit it in the same PR. CI fails on any diff (AGENTS.md §5.8).
- Never widen "verified against" into "supports": the manifest states the versions this repository actually restores from, nothing more.
