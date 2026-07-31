# Phase 0 proof of concept (THROWAWAY)

This is the ROADMAP.md Phase 0 spike: restore a `pg_dump` custom-format file
into a disposable Docker container, wait for readiness, run one validation
query, tear down. Its only purpose is to surface real-world friction before
the adapter protocol and evidence schema are frozen. **None of this code is
production code**; it will be deleted or rewritten once `internal/` exists.
No tests on purpose — the deliverable is the findings list below, not the code.

## Run

```sh
./poc/make-fixture.sh     # one-time: generates poc/testdata/orders.dump (100k rows)
go run ./poc              # the drill; JSON result on stdout, logs on stderr
```

No ports are published; all queries run via `docker exec` inside the sandbox.
Containers carry the label `com.probavi.poc=1`; orphans from crashed runs are
swept on startup.

## Findings (feed these into docs/ before v0 freeze)

Verified 2026-07-30 on Docker 29.1.3, postgres:16, 100k-row fixture. Paths
exercised: happy path, corrupt dump, wall-clock timeout killing the drill
mid-transfer. Zero orphaned containers after all runs.

1. **Readiness detection is engine-specific and full of traps.** The official
   postgres image's initdb phase runs a *temporary* server listening on the
   unix socket only, so a socket-based `pg_isready` reports ready before the
   real server is up. A TCP check (`pg_isready -h 127.0.0.1`) was reliable
   (~1.2 s to ready, restore always succeeded immediately after).
   → adapter-protocol: readiness belongs to the adapter (`healthcheck`); the
   core owns only wall-clock timeouts. Feeds the "timeout ownership" TODO.

2. **"Restore duration" is ambiguous — define timing phases precisely.** Of
   the measured 1.47 s, ~1.17 s was container start + readiness wait and only
   ~0.3 s was transfer + `pg_restore`. If `restore_seconds` drives the RTO
   trend feature, the schema must split timings: `provision` (sandbox up +
   ready), `transfer` (source into sandbox), `restore` (engine restore),
   `validate`. Lumping them makes the metric meaningless.
   → evidence-schema: replace the two-field `timings` sketch with per-phase
   fields.

3. **Teardown must run on its own context.** Cleanup tied to the drill
   context is skipped exactly when it matters most (timeout/interrupt).
   Verified: the context killed `docker cp` mid-flight; teardown on a fresh
   30 s context still removed the container.
   → adapter-protocol: core MUST call `teardown` after any failure/timeout
   with a separate grace budget; `teardown` MUST be idempotent.

4. **Getting the backup into the sandbox is a real design decision.**
   `docker cp` means a full extra copy of the source — fine at 552 KB,
   painful at 500 GB. Copy vs bind-mount vs streaming affects duration,
   disk, and isolation.
   → adapter-protocol: the "how sandbox handles are passed" TODO must also
   specify how source data reaches the adapter inside the sandbox.

5. **Engine tools' error semantics need pinning.** `pg_restore` by default
   continues past errors and merely warns ("errors ignored on restore");
   the PoC forces `--exit-on-error`. A corrupt input produced a clean
   nonzero exit with a clear message.
   → adapter-protocol: error-code registry needs `source_corrupt` etc.;
   adapters must map engine tool failures strictly — no silent partial
   restores.

6. **Zero-ingress sandboxing works.** No published ports; validation ran via
   `docker exec psql` inside the sandbox boundary.
   → sandbox contract: default to no network exposure; checks execute
   in-sandbox.

7. **Label-based orphan sweep is cheap and effective**
   (`docker ps -aq --filter label=...`): confirmed as the cleanup pattern
   for the docker provider.
