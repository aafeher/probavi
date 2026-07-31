# probavi-adapter-postgres

The PostgreSQL engine adapter for Probavi, implementing `probavi-adapter/0`
(see `docs/adapter-protocol.md`). Standard library only — deliberately no
imports from the Probavi core, as proof that the protocol document alone is
enough to build an adapter.

## Supported source kinds

| Kind         | Meaning                                                        |
|--------------|----------------------------------------------------------------|
| `pgdump`     | One `pg_dump` custom-format (`-Fc`) file.                      |
| `pgdump_dir` | A directory of dump files; the newest regular file is restored (mtime, ties broken by name). |
| `pgbackrest` | A pgBackRest repository directory (filesystem repo) — a physical restore. |

Planned: PITR (`--type=time`) on top of `pgbackrest` — Phase 2.

## The pgbackrest kind (physical restore)

A pgBackRest restore replaces the data directory, so the engine must not be
running when the drill starts. Requirements:

- **Sandbox image** containing `postgres`, `pgbackrest`, and `gosu`
  (e.g. built `FROM postgres:16` + `apt-get install pgbackrest`).
- **Idle start**: the sandbox must not boot the engine — with the docker
  provider set `command: sleep infinity` in `sandbox.params`. The adapter
  refuses to run against an already-running engine.
- **`source.params.stanza`**: the stanza name inside the repository
  (letters, digits, `-`, `_`).

The adapter transfers the repo into the sandbox, writes
`/etc/pgbackrest/pgbackrest.conf`, restores as the `postgres` user, then
**overwrites `pg_hba.conf` with sandbox-local trust auth** before starting
the server: the restored cluster's own auth config expects credentials the
drill does not have, and the sandbox has no network exposure whatsoever
(`--network none`, no published ports), so trust never extends beyond the
disposable container. Recovery replays WAL from the repo's archive; the
measured `engine_ready` phase covers server start plus recovery.

## Backup identity

- `checksum`: SHA-256 over the selected artifact's bytes (for `pgdump_dir`,
  the chosen file). For `pgbackrest` repositories: a canonical tree hash —
  entries sorted by relative path; each regular file contributes
  `relpath NUL size NUL content`, each symlink `relpath NUL "L" target NUL`.
- `created_at`: the artifact's modification time (for repositories: the
  newest file's mtime) — the closest derivable stand-in for the backup's
  creation time; treat it accordingly if you copy backup files around
  without preserving timestamps.

## Drill config options

| Option     | Default    | Meaning                              |
|------------|------------|--------------------------------------|
| `user`     | `postgres` | Superuser inside the sandbox engine. |
| `database` | `postgres` | Database to restore into.            |

## Environment

Credentials for reading backup sources arrive via the environment variables
declared in the drill config's `source.credential_env` (none are needed for
local files). Secrets never appear in protocol messages or logs.

## Behavior notes

- Engine readiness is probed over TCP (`pg_isready -h 127.0.0.1`): during
  initdb the official image runs a temporary server on the unix socket
  only, so socket probes would report ready too early.
- Restores run `pg_restore --no-owner --exit-on-error`: partial restores
  fail loudly (`restore_failed`), unreadable archives are classified as
  `source_corrupt`.
- `teardown` has nothing to release — everything this adapter creates lives
  inside the sandbox, which the provider destroys.
