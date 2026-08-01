# probavi-adapter-mssql

The Microsoft SQL Server engine adapter for Probavi, implementing
`probavi-adapter/0` (see `docs/adapter-protocol.md`). Standard library
only — deliberately no imports from the Probavi core; like the other
in-repo adapters, it is written from the protocol document alone.

## Supported source kinds

| Kind      | Meaning                                                       |
|-----------|---------------------------------------------------------------|
| `bak`     | One native `BACKUP DATABASE ... TO DISK` file.                |
| `bak_dir` | A directory of backup files; the newest regular file is restored (mtime, ties broken by name). |

## Sandbox image: start it idle

```yaml
sandbox:
  provider: docker
  params:
    image: mcr.microsoft.com/mssql/server:2022-latest
    command: sleep infinity
```

The idle start is **required**, and the reason is evidence integrity, not
convenience: SQL Server refuses to run without a superuser password, the
image only accepts one through environment variables, and sandbox params
are recorded verbatim in signed evidence records — where credentials must
never appear. So the adapter starts `sqlservr` itself and owns the engine
lifecycle. A sandbox whose engine is already running with its own
credentials is refused with a clear error.

By configuring this image you accept Microsoft's EULA for it; the adapter
passes `ACCEPT_EULA=Y` on your behalf when starting the server.

## The sandbox password is a documented constant

The drill engine's `sa` password is `Probavi!DrillSandbox0` — a **public
constant compiled into the adapter**, not a secret. It cannot be the
core's ephemeral per-drill secret: the protocol forbids secret values in
any protocol message (§2.5), and setting an engine password requires
sending the value through one. This is the SQL Server equivalent of the
postgres adapter's `pg_hba` trust overwrite and the mysql adapter's empty
root password: publicly known access, acceptable **only** because Probavi
sandboxes have zero ingress — `--network none`, and publishing ports is
not expressible at all. The credential never protects anything reachable.

## Restore behavior

- The backup's file list is read with `RESTORE FILELISTONLY` and every
  logical file is `MOVE`d to a fresh path under `/var/opt/mssql/data/` —
  the paths inside the `.bak` belong to the production server, not this
  sandbox. Logical names are quoted defensively on the way into T-SQL.
- The database is restored **under the drill's target name** (default
  `probavi`, override with `target.options.database`), regardless of the
  name it carried in the backup — `connection.database` and your checks
  point at it directly.
- Backup media the engine rejects (Msg 3241 "incorrectly formed",
  Msg 3254 "volume ... is empty", Msg 3242 "not a valid ... backup set")
  is classified `source_corrupt`; restores that run and fail are
  `restore_failed`.

## The dialect bridges

The core validates and quotes check identifiers in SQL-standard form
(`SELECT count(*) FROM "orders"`) and expects undecorated result rows.
The declared `sql_runner` absorbs both SQL Server quirks declaratively
(§6.1), so builtin checks work unchanged:

- `-I` turns on `QUOTED_IDENTIFIER`, accepting double-quoted identifiers
  (sqlcmd's default is off);
- `SQLCMDINI` points at a startup script the adapter writes during
  provision (`SET NOCOUNT ON`), which removes the `(N rows affected)`
  trailer from sqlcmd's stdout — the sqlcmd equivalent of the mysql
  adapter's `--init-command` bridge.

Checks are plain T-SQL against the restored database.

## Backup identity

- `checksum`: SHA-256 over the selected artifact's bytes (for `bak_dir`,
  the chosen file).
- `created_at`: the artifact's modification time — the closest derivable
  stand-in for the backup's creation time; treat it accordingly if you
  copy backup files around without preserving timestamps.

## Drill config options

| Option     | Default   | Meaning                                        |
|------------|-----------|------------------------------------------------|
| `database` | `probavi` | Name the backup is restored under (letters, digits, underscores, hyphens only). |

## Environment

Credentials for reading backup sources arrive via the environment variables
declared in the drill config's `source.credential_env` (none are needed for
local files). Secrets never appear in protocol messages or logs.

## Behavior notes

- The image runs as the non-root `mssql` user; the docker provider's
  `put_file` lands the backup owned by that user (this adapter is why).
- Point-in-time recovery (`STOPAT` over log backups) is not supported
  yet; the probe declares `pitr: false` for both kinds.
- `teardown` has nothing to release — everything this adapter creates
  lives inside the sandbox, which the provider destroys.
