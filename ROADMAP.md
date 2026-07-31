# Probavi Roadmap

Order matters. Each phase has an explicit exit criterion; do not start the next phase before meeting it. The two specifications in Phase 0 are the foundation everything else depends on — rushing them is the most expensive mistake available.

## Phase 0 — Contracts before code

**Goal:** the two load-bearing specifications exist and have survived review.

- [x] Choose project name: **Probavi** (Latin: "I have proven"). Pre-checked 2026-07-30: npm, PyPI, crates.io free; zero exact-name GitHub repos; no company hits in web search.
- [x] Canonical domain: **probavi.dev registered 2026-07-31.**
- [x] Choose license and add `LICENSE`. **Decided 2026-07-31: open-core model. This repository (the core) is Apache-2.0; Phase 3 organisational features will be offered commercially, developed outside this repository. The evidence format spec and the independent verifier stay freely available forever — paywalling verification would destroy the trust proposition. Contributions: DCO.**
- [x] Write `docs/adapter-protocol.md` v0: JSON-over-stdio protocol with four operations — `probe`, `provision`, `healthcheck`, `teardown` — including full example request/response messages, error model, timeout semantics, and versioning rules. **Approved 2026-07-31 (core-mediated sandbox-verb model); normative. JSON Schema files + conformance test list remain before final freeze (tracked in the doc §11).**
- [x] Write `docs/evidence-schema.md` v0: record fields, canonical JSON serialization, hash-chain construction, ed25519 signing, verification procedure. **Approved 2026-07-31 (RFC 8785 JCS restricted to integer-only numbers); normative. JSON Schema file + golden example log remain before final freeze (tracked in the doc §11).**
- [x] Proof of concept (~50–100 lines Go, may be throwaway): restore a `pg_dump` file into a Docker container, wait for readiness, run one `SELECT count(*)`, tear down. Purpose: surface real-world friction (Docker API, readiness detection, cleanup on failure) before designing around imagined problems. **Done 2026-07-30 — see `poc/README.md` for the seven findings that must be folded into the two specs.**

**Exit criterion:** a stranger could implement an adapter from the protocol doc alone, and could verify an evidence record from the schema doc alone.

## Phase 1 — MVP: the best PostgreSQL restore verifier in the world

**Goal:** a single `probavi` binary, usable in production by a small team, PostgreSQL only, Docker sandbox only.

- [x] `internal/config`: YAML drill config — parsing, validation, helpful error messages. **Complete 2026-07-31: strict parsing (unknown field / duplicate key = annotated error via goccy/go-yaml), all validation problems reported in one pass, per-builtin check rules, config hash for evidence records; the example config is loaded by the test suite so it cannot drift.**
- [x] `internal/sandbox` (docker provider): create/start/await-ready/destroy; guaranteed cleanup even on crash (labels + startup sweep of orphans). **Complete 2026-07-31: contract types in `internal/sandbox`, provider in `internal/sandbox/docker` (docker CLI, no SDK dependency). Zero-ingress default (`--network none`, ports not expressible), owner-pid labels so the sweep spares concurrent drills, idempotent destroy, exec/put_file verbs with protocol §4 capture caps. Unit tests via injected runner + real-Docker integration suite.**
- [x] `adapters/postgres`: first adapter implementing the protocol; sources: plain `pg_dump` custom-format file, directory of dumps (pick latest), pgBackRest repo. **Complete 2026-07-31. Core-side protocol client (`internal/adapter`): process lifecycle with SIGTERM→grace→SIGKILL and a pipe-closing watchdog, NDJSON framing with the 4 MiB limit, request_id/protocol echo enforcement, sandbox-verb mediation with put_file allow-listing, env allowlist, adapter_crash classification — tested against scripted fake adapters covering every framing violation. Adapter (stdlib-only, zero core imports — written from the protocol doc alone): `pgdump` + `pgdump_dir` logical restores, and `pgbackrest` physical restores — idle sandbox via the provider's generic command override, canonical repository tree-hash for backup identity, restore + WAL recovery driven entirely in-sandbox, sandbox-local trust auth documented. End-to-end integration tests prove both paths against real Docker: pg_dump restore with sql_runner row validation, and a real pgBackRest repo (stanza-create + full backup) restored and queried through the full stack.**
- [x] `internal/checks`: built-ins — `service_healthy`, `table_exists`, `row_count` (min/max), `freshness` (newest row in column younger than X); plus user-defined SQL assertions with expected results. **Complete 2026-07-31: everything runs through the adapter-declared sql_runner template (zero engine knowledge in the core); strict identifier validation + quoting (injection-proof by construction); freshness compares max(column) in Go to avoid dialect-specific interval syntax (naive timestamps read as UTC); details follow the evidence redaction rules — aggregates yes, query result values never; per-check verdicts with infra-failure abort semantics.**
- [x] `internal/evidence`: append-only JSONL store, hash chain, ed25519 signing; `probavi evidence verify` subcommand. **Complete 2026-07-31: library (canonicalization, chain, signing, store with locking + torn-tail handling, verification; 95%+ coverage, golden + tamper tests) plus `probavi evidence verify` and `probavi evidence keygen` subcommands with schema §9 exit codes.**
- [x] `internal/metrics`: Prometheus textfile/HTTP exposition — `probavi_restore_duration_seconds`, `probavi_checks_passed`, `probavi_last_success_timestamp_seconds`. **Textfile exposition complete 2026-07-31 (plus checks_total and last_run timestamp; atomic rename, no client-library dependency). HTTP exposition deferred until a real need appears.**
- [x] CLI: `probavi run`, `probavi evidence verify`, `probavi adapter probe`. Meaningful exit codes for cron/CI. No built-in scheduler — document cron and systemd-timer usage instead (lock file, timeout, overlap protection). **Complete 2026-07-31: `run` (config-driven drill with hard wall-clock limit, SIGTERM→cancelled record, exit codes 0/1/2/3/5), `evidence verify`/`keygen`, `adapter probe`; cron + flock usage documented in README. `internal/core` orchestrator ties config→sandbox→adapter→checks→evidence with guaranteed teardown and an always-appended signed record; end-to-end CLI integration test covers pass, corrupt-backup fail, offline verify, and zero leftover sandboxes.**
- [x] Docs + README polish, example configs, quickstart that works in under 10 minutes. **Done 2026-07-31: README quickstart (clone → build → keygen → drill → offline verify, with a demo-dump fallback) verified by executing it verbatim from a fresh clone — 4 seconds with warm caches; well under 10 minutes cold.**
- [x] Publish on GitHub. **Done 2026-07-31: https://github.com/aafeher/probavi — all four CI gates green on the public repo.**
- [ ] **Announce (r/PostgreSQL, HN, lobste.rs).** Early feedback outranks any additional feature.

**Exit criterion:** a stranger goes from `git clone` to a verified restore of their own Postgres backup in under 10 minutes.

## Phase 2 — Prove the abstractions

**Goal:** demonstrate that both extension axes (engines, sandboxes) work without touching the core.

- [x] Second adapter: MySQL/MariaDB (`mysqldump` + one physical-backup source). **Test of the protocol: if the core needs changes, fix the protocol now — it is still cheap.** **Complete 2026-07-31 (`adapters/mysql`: `mysqldump` + `mysqldump_dir` logical, Percona XtraBackup physical) with zero core changes. The protocol passed its test: the dialect difference (MySQL rejects SQL-standard quoted identifiers) is absorbed declaratively by the adapter's `sql_runner` template (`ANSI_QUOTES` via `--init-command`), and the physical flow reuses the idle-sandbox pattern with an `--init-file` auth reset mirroring the postgres adapter's pg_hba trust overwrite. Both paths proven end to end against real Docker.**
- [ ] Point-in-time recovery drills: "restore to yesterday 14:32" for Postgres (WAL replay via wal-g or pgBackRest).
- [ ] Second sandbox provider: Kubernetes Job **or** remote host over SSH (pick based on user demand).
- [ ] Restore-duration trend data surfaced in metrics; alert-friendly derived metrics (e.g. rolling P95 restore time).
- [ ] Adapter developer guide + conformance test suite (`probavi adapter conformance <cmd>`), so third parties can build adapters confidently.

**Exit criterion:** adding engine #3 requires zero core changes; an externally-authored adapter passes conformance.

## Phase 3 — The differentiating layer

**Goal:** turn verified runs into organisational trust.

**Open-core boundary (decided 2026-07-31):** proving a single drill stays free forever — the CLI, adapters, evidence chain, verify, and metrics in this repository, Apache-2.0. The organisational layer below — fleet dashboard, audit report export, RTO/RPO objectives and trends, SSO/RBAC — will be offered commercially, built on top of this open core and documented transparently when Phase 3 starts. The evidence spec and the independent verifier are never commercial.

- [ ] Fleet dashboard (read-only web UI over the evidence store): estate-wide recoverability status, trends, drill calendar.
- [ ] Audit report export (HTML/PDF): "in the last 90 days, N drills across M databases, success rate, measured RTO distribution" — mapped to DORA / NIS2 / NIST expectations (see AGENTS.md §Compliance).
- [ ] RTO/RPO objectives in config; automatic breach detection and trend-based early warning ("restore time will exceed your RTO within ~6 months at current growth").
- [ ] Publish the evidence format as a standalone versioned spec with an independent verifier tool.

**Exit criterion:** a compliance officer can hand an Probavi report to an auditor without editing it.

## Phase 4 — Ecosystem

- [ ] MongoDB adapter; SQL Server adapter (community-driven if possible).
- [ ] DR game-day orchestration: multi-database, dependency-ordered restore drills.
- [ ] Notification integrations (webhook first; Slack/email via webhook recipes).
- [ ] Internationalization (i18n): make user-facing output — CLI messages, dashboard UI, audit report exports — localizable into any national language. English remains the canonical source language; evidence records, specs, and the codebase stay English-only (they are canonical machine/contract formats, not UI).
- [ ] Evaluate hosted/managed offering (business decision, out of scope for the OSS core).

## Deliberate non-goals (all phases)

No backup engine. No built-in scheduler. No agent daemons on database hosts unless proven unavoidable. No more than one new engine per release cycle. No UI before Phase 3. Every "no" protects the quality of the core.
