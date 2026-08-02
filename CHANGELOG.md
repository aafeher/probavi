# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0, minor versions may contain breaking changes; every one of them is
recorded here. The adapter protocol and the evidence schema carry their own
versions, independent of the binary (see `docs/`); changes to either are
always called out explicitly.

## [Unreleased]

### Added

- Internationalized CLI output (spec docs/i18n.md): the usage text and
  CLI diagnostics are now localizable, with Hungarian (`hu`) as the
  first national language. The locale comes from
  `PROBAVI_LANG → LC_ALL → LC_MESSAGES → LANG` (POSIX order, no new
  flags or config keys); English remains the default and the fallback
  for anything unknown. Catalogs are zero-dependency embedded JSON
  keyed by the English text itself, and CI gates enforce completeness,
  staleness, and format-verb parity per language — a partially
  translated language cannot ship. Machine contracts are never
  translated: evidence records, JSON summaries, the adapter protocol,
  notification payloads, and structured logs stay English.
  Configuration-validation diagnostics (drill and game-day files) are
  part of the translated surface: `probavi run --config broken.yaml`
  explains the problem in the operator's language, with config keys and
  locators kept verbatim.
- DR game-day orchestration (`probavi gameday`, spec docs/gameday.md):
  multi-database restore exercises in dependency order. A game-day
  config references normal drill files as members with `depends_on`
  edges; each member runs the full drill pipeline — sandbox, restore,
  checks, its own signed evidence record, metrics, notifications — so
  the evidence schema is untouched and members stay independently
  runnable. Dependents of a failed member are skipped with a recorded
  reason (cascading), independent branches always run to completion,
  and cancellation leaves signed `cancelled` records for running
  members. Execution is sequential by default; `max_parallel` opts in
  to bounded concurrency, with a load-time guard rejecting members
  that share an evidence log while allowed to overlap. The one-line
  JSON summary lists every member with its record location
  (evidence path + seq) and reports the end-to-end wall clock — the
  service-level recovery time. Exit codes: 0 all passed, 1 a member
  drill failed, 2 errors/cancellation left members unproven, 5 a
  member's record could not be written.
- Webhook notifications (`notify` config section, docs/notifications.md):
  one JSON POST per configured webhook after the evidence record is
  signed, carrying the `probavi-notification/1` payload — a signpost to
  the record (outcome, check counts, restore timing, sequence number),
  never a substitute for it. URLs come from config or, for token-bearing
  endpoints, from the environment (`url_env`) and are redacted from all
  logs and errors; optional HMAC signing (`secret_env`,
  `X-Probavi-Signature-256`) lets receivers authenticate pushes. An `on`
  filter narrows delivery per outcome; the default — every outcome —
  keeps dead-man's-switch receivers working. Delivery is bounded (60 s
  budget, 3 attempts, no redirects), runs outside the drill timeout so
  cancelled drills still notify, and never changes the drill's verdict
  or exit code. The payload has a machine-readable schema
  (`docs/schemas/notification/payload.json`) validated in CI.
- SQL Server adapter (`adapters/mssql`, `probavi-adapter-mssql`):
  restores native `BACKUP DATABASE` artifacts (`bak`, `bak_dir` kinds)
  under the drill's target name, with the file list read from the backup
  and every logical file `MOVE`d to sandbox paths. The sandbox starts
  idle and the adapter owns the engine: SQL Server cannot run without a
  superuser password, and a password in sandbox params would enter the
  signed evidence record — so the drill engine uses a documented public
  constant confined to the zero-ingress sandbox, the mssql analog of the
  postgres trust overwrite and the mysql empty root password. sqlcmd's
  dialect quirks are absorbed declaratively (`-I` for double-quoted
  identifiers, a `SQLCMDINI` startup script for undecorated rows), so
  builtin checks work unchanged. Conformance 15/15.
- MongoDB adapter (`adapters/mongodb`, `probavi-adapter-mongodb`):
  restores `mongodump --archive` backups — plain or `--gzip`, the
  compression sniffed from the artifact bytes — with
  `mongorestore --stopOnError`, so partial restores fail loudly. Source
  kinds: `mongodump` (one archive file) and `mongodump_dir` (newest file
  in a directory). Checks are mongosh `--eval` expressions carried by the
  declared sql_runner template; the core stays engine-free, and the
  adapter passes all 15 conformance checks. Third engine, zero core
  changes.
- `remotehost` sandbox provider: restore drills on dedicated hosts that
  cannot run any container runtime, over plain SSH + systemd
  (`docs/sandbox-bare-host.md`). One sandbox is one transient systemd
  slice plus one per-drill workspace; every command — including the
  engine the adapter starts — runs as a transient unit inside the slice,
  so resource caps bound the whole sandbox and stopping the slice kills
  the entire process tree. Cleanup is three-layered (destroy on every
  outcome, host-scoped orphan sweep, target-side deadline timer that
  survives a vanished drill host). The target is selected with
  `PROBAVI_SSH_TARGET` in the environment only; connection details never
  enter drill config or evidence records.
- Remote Docker over SSH: the docker sandbox provider is documented and
  CI-proven against remote daemons selected with `DOCKER_HOST=ssh://…` —
  drills run on the remote machine while backups stream through the SSH
  connection, never a published port. The endpoint lives in the
  environment only; connection details never enter evidence records.

### Fixed

- The docker provider's `put_file` lands files owned by the identity
  exec commands run as (root-run chown after the copy), matching the k8s
  and remotehost providers, where the exec user creates the file by
  construction. Previously `docker cp` preserved the host file's numeric
  uid, so on images with a non-root default user (SQL Server runs as
  `mssql`) the copied backup was unreadable by the engine and the mode
  step failed outright. `put_file` on the docker provider now requires
  `sh` in the image, like the other providers always did.
- The docker orphan sweep is host-scoped now (matching the k8s provider):
  sandboxes carry a `com.probavi.host` label, and when several drill
  hosts share one daemon, a host can no longer mistake another host's
  live drill for a dead orphan and sweep it mid-run.

## [0.1.0] - 2026-08-01

First tagged release. Everything below is new.

### Added

- `probavi run`: config-driven restore drills — disposable sandbox up,
  backup restored by an engine adapter, validation checks, guaranteed
  teardown, and exactly one signed evidence record per started drill, on
  every path including crashes and cancellation. Cron/CI-friendly exit
  codes; no built-in scheduler by design.
- **Adapter protocol v0** (`docs/adapter-protocol.md`, frozen): engine
  adapters are external processes speaking line-delimited JSON on
  stdin/stdout, acting on the sandbox only through core-mediated verbs
  (`exec`, `put_file`). Machine-readable JSON Schemas for every message
  shape in `docs/schemas/adapter/`.
- **PostgreSQL adapter**: `pgdump`, `pgdump_dir` (logical), `pgbackrest`
  (physical) sources; point-in-time recovery on pgBackRest sources.
- **MySQL/MariaDB adapter**: `mysqldump`, `mysqldump_dir` (logical),
  `xtrabackup` (Percona XtraBackup, physical) sources.
- Point-in-time recovery drills: `target.pitr` drill config with exactly
  one of `target_time` (absolute) or `target_age` (relative, resolved at
  drill start); the resolved instant is recorded in evidence as
  `drill.pitr_target`.
- Sandbox providers: **Docker** (zero-ingress defaults: no published
  ports, `--network none`) and **Kubernetes Job** (no service-account
  token, cluster-side cleanup backstop); both drive the respective CLI,
  label every resource, and sweep orphans on startup.
- **Evidence store** (`docs/evidence-schema.md` v1, frozen): append-only
  JSONL, RFC 8785 canonical bytes, SHA-256 hash chain, ed25519 signatures.
  `probavi evidence verify` proves a log offline with only the public key;
  `probavi evidence keygen` generates key pairs. Machine-readable schema
  covering all published record versions in `docs/schemas/evidence/`.
- Validation checks: `service_healthy`, `table_exists`, `row_count`,
  `freshness`, and user-defined SQL assertions — engine-agnostic via the
  adapter-declared `sql_runner` template, redaction-safe by construction.
- `probavi adapter conformance`: the frozen 15-check protocol conformance
  suite against a simulated sandbox — no container runtime needed; the
  mechanical definition of done for third-party adapters, with the
  developer guide in `docs/adapter-development.md`.
- `probavi adapter probe`: resolve an adapter and print its capabilities.
- Prometheus textfile metrics per drill, including rolling restore-duration
  quantiles (p50/p95/max over the last 100 restores) for RTO trend
  alerting.
- `probavi version`: prints the binary version and the contract versions
  the build speaks.

[Unreleased]: https://github.com/aafeher/probavi/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/aafeher/probavi/releases/tag/v0.1.0
