# probavi-adapter-mysql

The MySQL engine adapter for Probavi, implementing `probavi-adapter/0`
(see `docs/adapter-protocol.md`). Standard library only — deliberately no
imports from the Probavi core; like the postgres adapter, it is written
from the protocol document alone.

## Supported source kinds

| Kind            | Meaning                                                     |
|-----------------|-------------------------------------------------------------|
| `mysqldump`     | One `mysqldump` SQL file.                                   |
| `mysqldump_dir` | A directory of dump files; the newest regular file is restored (mtime, ties broken by name). |
| `xtrabackup`    | A Percona XtraBackup full-backup directory (unprepared, as `xtrabackup --backup` leaves it) — a physical restore. |

## Sandbox image and authentication

The sandbox image must contain `mysqld`, the `mysql` client, and
`mysqldump`-compatible tooling — the official `mysql:8.x` images do. The
adapter connects as the configured superuser without a password, so the
image must allow it:

```yaml
sandbox:
  provider: docker
  params:
    image: mysql:8.4
    env.MYSQL_ALLOW_EMPTY_PASSWORD: "yes"
```

An empty root password is acceptable **only** because Probavi sandboxes
have zero ingress by default: `--network none`, and publishing ports is not
expressible at all. The credential never protects anything reachable.

## Restore behavior

- The target database (default `probavi`, override with
  `target.options.database`) is created with `CREATE DATABASE IF NOT
  EXISTS` before the load: plain `mysqldump` output carries no `CREATE
  DATABASE` statement. For dumps taken with `--databases` (which embed
  `CREATE DATABASE`/`USE`), set `options.database` to the dumped schema
  name so the connection info and checks point at the restored data.
- The dump is loaded with the mysql client's `source` command, which stops
  at the first error: partial restores fail loudly as `restore_failed`;
  input the parser rejects (`ERROR 1064`, `ASCII '\0'`) is classified
  `source_corrupt`.

## The xtrabackup kind (physical restore)

An XtraBackup restore replaces the data directory, so the engine must not
be running when the drill starts. Requirements:

- **Sandbox image** containing `mysqld`, `xtrabackup`, and `gosu`, with the
  XtraBackup major version matching the server (e.g. built
  `FROM mysql:8.0-debian` + `percona-xtrabackup-80` from the Percona apt
  repository — the integration test builds exactly this).
- **Idle start**: the sandbox must not boot the engine — with the docker
  provider set `command: sleep infinity` in `sandbox.params`. The adapter
  refuses to run against an already-running engine.
- **Unprepared full backup**: the source directory is what
  `xtrabackup --backup --target-dir=...` produced; the adapter runs
  `--prepare` and `--copy-back` itself (both timed as restore work). A
  directory without `xtrabackup_checkpoints` is refused as
  `source_corrupt` before any transfer.

The restored grant tables carry **production credentials the drill does
not have**, so the adapter starts the server with an `--init-file` that
resets `'root'@'%'` to an empty password with full privileges — the MySQL
equivalent of the postgres adapter's `pg_hba.conf` trust overwrite. The
sandbox has no network exposure whatsoever (`--network none`, no published
ports), so this access never extends beyond the disposable container.

Physical mode ignores `options.user`/`options.database`: the connection is
always `root` on the `mysql` system schema (the only database guaranteed
to exist in an arbitrary restored server). Point checks at restored data
with schema-qualified table names (e.g. `table: shop.orders`).

## The ANSI_QUOTES bridge

The core validates and quotes check identifiers in SQL-standard form
(`SELECT count(*) FROM "orders"`). MySQL's default `sql_mode` does not
accept double-quoted identifiers, so the declared `sql_runner` template
appends `ANSI_QUOTES` to the session `sql_mode` via `--init-command`. The
engine dialect is absorbed by the adapter's declaration — the core stays
engine-free, which is exactly what the protocol's sql_runner template
exists for (§6.1).

## Backup identity

- `checksum`: SHA-256 over the selected artifact's bytes (for
  `mysqldump_dir`, the chosen file). For `xtrabackup` directories: a
  canonical tree hash — entries sorted by relative path; each regular file
  contributes `relpath NUL size NUL content`, each symlink
  `relpath NUL "L" target NUL`.
- `created_at`: the artifact's modification time (for backup directories:
  the newest file's mtime) — the closest derivable stand-in for the
  backup's creation time; treat it accordingly if you copy backup files
  around without preserving timestamps.

## Drill config options

| Option     | Default   | Meaning                               |
|------------|-----------|---------------------------------------|
| `user`     | `root`    | Superuser inside the sandbox engine (logical restores only). |
| `database` | `probavi` | Database to restore into (letters, digits, underscores only; logical restores only). |

## Environment

Credentials for reading backup sources arrive via the environment variables
declared in the drill config's `source.credential_env` (none are needed for
local files). Secrets never appear in protocol messages or logs.

## Behavior notes

- Engine readiness is probed with a TCP `SELECT 1`: the official image's
  first-boot initialization runs a temporary server with
  `--skip-networking` (socket only), so a TCP probe cannot report ready
  too early — the same trap as PostgreSQL's initdb-phase server.
- MariaDB is expected to work through the same client tooling but is
  untested; treat it as out of scope until it has its own verified
  integration coverage.
- `teardown` has nothing to release — everything this adapter creates
  lives inside the sandbox, which the provider destroys.
