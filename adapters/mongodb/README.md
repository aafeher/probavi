# probavi-adapter-mongodb

The MongoDB engine adapter for Probavi, implementing `probavi-adapter/0`
(see `docs/adapter-protocol.md`). Standard library only — deliberately no
imports from the Probavi core; like the postgres and mysql adapters, it is
written from the protocol document alone.

## Supported source kinds

| Kind             | Meaning                                                    |
|------------------|------------------------------------------------------------|
| `mongodump`      | One `mongodump --archive` file, plain or `--gzip` — the compression is sniffed from the bytes, never from the file name. |
| `mongodump_dir`  | A directory of archive files; the newest regular file is restored (mtime, ties broken by name). |

The directory-tree format (`mongodump` without `--archive`) is not
supported: take archives (`mongodump --archive=backup.archive --gzip`) —
one artifact, one checksum, one identity in the evidence record.

## Sandbox image and authentication

The sandbox image must contain `mongod`, `mongosh`, and `mongorestore` —
the official `mongo:6`/`mongo:7`/`mongo:8` images do. Run the image
**bare**, with no `MONGO_INITDB_*` variables:

```yaml
sandbox:
  provider: docker
  params:
    image: mongo:7
```

mongod then starts without access control. That is acceptable **only**
because Probavi sandboxes have zero ingress by default: `--network none`,
and publishing ports is not expressible at all. It also skips the image's
first-boot initialization phase (a temporary localhost-only server), so
readiness probes cannot be fooled by a server that is about to restart.
Because no authentication exists in the sandbox, `connection.user` is
reported empty and the declared `sql_runner` references no `{{user}}`.

## Restore behavior

- The archive is replayed with `mongorestore --stopOnError`: partial
  restores fail loudly as `restore_failed` (§5) instead of being papered
  over by the tool's default keep-going behavior. Databases and
  collections are restored under their original names from the archive.
- Input mongorestore rejects as an archive (wrong magic number, "does not
  appear to be a mongodump archive", a lying gzip header) is classified
  `source_corrupt`.
- `options.database` (default `admin`) selects the database that
  `connection.database`, the healthcheck ping, and the sql_runner target —
  set it to the database your checks query (typically the one the archive
  restores). It never influences what gets restored.

## Checks: the mongosh --eval dialect

MongoDB has no SQL. The declared `sql_runner` template runs the check text
through `mongosh --quiet --eval`, so **checks for this adapter are mongosh
expressions**, written in the drill config's raw `sql` field:

```yaml
checks:
  - name: orders_present
    sql: "db.orders.countDocuments({})"
    min: 1
```

The expression's result is printed as the row output (a number prints as a
bare number). For multi-column rows, `print()` tab-separated values.
Builtin checks that generate SQL (`row_count`, etc.) do not apply to this
adapter — use raw expressions. This is the protocol's design working as
intended: the engine dialect is absorbed by the adapter's declared
template, and the core never learns it (§6.1).

## Backup identity

- `checksum`: SHA-256 over the selected artifact's bytes (for
  `mongodump_dir`, the chosen file). For gzip archives the hash covers the
  compressed bytes exactly as stored.
- `created_at`: the artifact's modification time — the closest derivable
  stand-in for the backup's creation time; treat it accordingly if you
  copy backup files around without preserving timestamps.

## Drill config options

| Option     | Default | Meaning                                          |
|------------|---------|--------------------------------------------------|
| `database` | `admin` | Database for connection info, healthcheck, and checks (letters, digits, underscores, hyphens only). |

## Environment

Credentials for reading backup sources arrive via the environment variables
declared in the drill config's `source.credential_env` (none are needed for
local files). Secrets never appear in protocol messages or logs.

## Behavior notes

- Engine readiness is probed with `db.runCommand({ping:1})` over a
  connection string that bounds server selection to 2 s, so an unready
  engine answers the poll quickly instead of hanging into the per-command
  timeout.
- Point-in-time recovery (oplog replay) is not supported yet; the probe
  declares `pitr: false` for both kinds.
- `teardown` has nothing to release — everything this adapter creates
  lives inside the sandbox, which the provider destroys.
