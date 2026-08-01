# Probavi

*Probavi* — Latin for **"I have proven."** The perfect tense is the point: not "we test restores", but "this restore was performed and proven, here is the signed record."

**You have backups. But when did you last prove they restore?**

Probavi is a self-hosted, engine-agnostic platform for **continuous restore verification**. It does not take backups — your existing tools (pg_dump, pgBackRest, wal-g, mysqldump, …) already do that well. Probavi's job is to continuously *prove* that those backups are actually recoverable:

1. On a schedule, it takes a real backup and performs a **real restore** into a disposable, isolated sandbox (e.g. a Docker container).
2. It runs **validation checks** against the restored database — from "did it start?" through row counts and data freshness to custom SQL assertions.
3. It records the outcome as a **signed, tamper-evident evidence record**: what was restored, when, how long it took, what was checked, and what the result was.

The output is not a green checkmark. It is an auditable, cryptographically verifiable history of your organisation's recoverability — including measured restore times (RTO) and their trend over time.

## Why

- The "backup completed successfully" log line proves almost nothing. Backups fail silently: corruption, missing WAL segments, version mismatches, lost encryption keys, wrong databases backed up for months.
- Regulations increasingly require *tested and documented* recovery capability, not just backups (see EU DORA, NIS2, and NIST contingency-planning guidance).
- Cloud providers offer restore testing for their own managed services. If you run databases on your own VMs, bare metal, or a mixed estate, there is no neutral, open tool that does this for you. Probavi is that tool.

## Status

**Pre-alpha, working end to end for PostgreSQL and MySQL.** `probavi run` restores real backups — logical dumps (`pg_dump`, `mysqldump`) and physical backups (pgBackRest, Percona XtraBackup) — into a disposable sandbox (Docker container or Kubernetes Job), validates them, and appends a signed evidence record; `probavi evidence verify` proves the log offline. Point-in-time recovery drills ("prove we can restore to 24 hours ago") work on pgBackRest sources, and the record carries the exact instant proven. The adapter protocol (v0) and evidence schema (v1) specs in `docs/` are normative and frozen, with machine-readable JSON Schemas in [docs/schemas/](docs/schemas/); third parties can build adapters in any language from [docs/adapter-development.md](docs/adapter-development.md) and validate them with `probavi adapter conformance` — no container runtime needed. Not yet released — packaging and polish remain. See [ROADMAP.md](ROADMAP.md) and [AGENTS.md](AGENTS.md).

## Shape

```yaml
# drill.yaml — a recovery drill as code (implemented; see examples/)
target:
  name: prod-orders-db
  adapter: postgres
  source:
    kind: pgdump
    path: /backups/orders/latest.dump
sandbox:
  provider: docker
  params:
    image: postgres:16
  timeout: 30m
checks:
  - builtin: service_healthy
  - builtin: row_count
    table: orders
    min: 100000
  - builtin: freshness
    table: orders
    column: created_at
    max_age: 24h
evidence:
  path: /var/lib/probavi/evidence.jsonl
  sign_key: /etc/probavi/ed25519.key
```

```console
$ probavi evidence keygen --out /etc/probavi/ed25519.key
$ probavi run --config drill.yaml
{"outcome":"pass","seq":42,"evidence_path":"/var/lib/probavi/evidence.jsonl","checks_passed":3,"checks_total":3,"restore_ms":252400,"total_ms":259100}
$ probavi evidence verify --log /var/lib/probavi/evidence.jsonl --key /etc/probavi/ed25519.key.pub
{"status":"VALID","records":42,"damaged_lines":[],"failed_line":0,"reason":""}
```

Exit codes are the cron/CI contract: `0` backup proven restorable, `1` recoverability failure, `2` infrastructure error, `5` evidence record could not be written.

## Quickstart

Prove a PostgreSQL backup restorable in about five minutes. You need Go 1.24+, Docker, and a `pg_dump` custom-format (`-Fc`) backup file.

```console
$ git clone https://github.com/aafeher/probavi.git && cd probavi
$ go build -o bin/probavi ./cmd/probavi
$ go build -o bin/probavi-adapter-postgres ./adapters/postgres
$ export PATH="$PWD/bin:$PATH"
$ bin/probavi evidence keygen --out probavi.key
```

Create `drill.yaml` — point `path` at your backup and pick the image matching your PostgreSQL major version:

```yaml
target:
  name: my-first-drill
  adapter: postgres
  source:
    kind: pgdump
    path: /path/to/your/backup.dump
sandbox:
  provider: docker
  params:
    image: postgres:16
    # trust auth is sandbox-only: the container runs with --network none
    # and no published ports; it is destroyed after the drill.
    env.POSTGRES_HOST_AUTH_METHOD: trust
  timeout: 30m
checks:
  - builtin: service_healthy    # add real checks: see examples/drill.example.yaml
evidence:
  path: evidence.jsonl
  sign_key: probavi.key
```

```console
$ probavi run --config drill.yaml
{"outcome":"pass","seq":1,...,"restore_ms":84,...}
$ probavi evidence verify --log evidence.jsonl --key probavi.key.pub
{"status":"VALID","records":1,...}
```

That `VALID` is the product: anyone holding only the log file and your public key can reproduce it, fully offline.

<details>
<summary>No backup at hand? Generate a demo dump.</summary>

```console
$ docker run -d --name probavi-demo -e POSTGRES_HOST_AUTH_METHOD=trust postgres:16
$ until docker exec probavi-demo pg_isready -h 127.0.0.1 -q; do sleep 1; done
$ docker exec probavi-demo psql -h 127.0.0.1 -U postgres -c "CREATE TABLE demo AS SELECT generate_series(1,100000) AS id;"
$ docker exec probavi-demo pg_dump -h 127.0.0.1 -U postgres -Fc -f /tmp/demo.dump postgres
$ docker cp probavi-demo:/tmp/demo.dump demo.dump && docker rm -f probavi-demo
```

Then set `path: demo.dump` in `drill.yaml`.
</details>

## Sandbox providers

The sandbox is where the restored copy of your production data briefly lives, so its defaults are deliberately locked down.

- **docker** — containers with `--network none` (loopback only), no published ports, labeled and force-removed with their volumes; an orphan sweep at every drill start reaps leftovers of crashed runs.
- **k8s** — each drill runs as a `batch/v1` Job (`kubectl` drives it; cluster selection follows `KUBECONFIG`):

  ```yaml
  sandbox:
    provider: k8s
    params:
      image: postgres:16
      namespace: probavi-drills   # default: "default"
      memory: 2Gi                 # requests == limits
      cpus: "2"
      env.POSTGRES_HOST_AUTH_METHOD: trust
    timeout: 30m
  ```

  The pod mounts no service-account token, declares no ports, and the Job carries `activeDeadlineSeconds` + `ttlSecondsAfterFinished`, so the cluster kills and garbage-collects the sandbox even if the drill host dies and never comes back. One residual difference to understand: Kubernetes pods always join the cluster network — pod-level isolation equivalent to Docker's `--network none` can only come from your cluster's NetworkPolicy. Every sandbox pod carries the label `com.probavi.sandbox=1`; give it a deny-all ingress/egress policy.

## Running on a schedule

Probavi deliberately has no built-in scheduler — cron or a systemd timer owns the cadence, Probavi owns the proof:

```
# /etc/cron.d/probavi — daily drill at 02:00, no overlapping runs
0 2 * * *  probavi  flock -n /run/probavi-orders.lock probavi run --config /etc/probavi/orders.yaml
```

The evidence store additionally holds its own single-writer lock, so overlapping drills against the same log fail fast instead of interleaving. Prometheus metrics land in the configured textfile for node_exporter — the last run's headline numbers plus rolling restore-duration quantiles recomputed from the evidence log itself (`probavi_restore_duration_rolling_seconds{quantile="0.5"|"0.95"|"1"}` over the last 100 restores). Two alert rules cover most needs: `time() - probavi_last_success_timestamp_seconds > 172800` ("no proven restore for two days") and `probavi_restore_duration_rolling_seconds{quantile="0.95"} > <your RTO>` ("restores are drifting past the objective"). Audit report export arrives in Phase 3.

## Design principles

- **Build on top of backup tools, never replace them.** Probavi orchestrates and verifies; pgBackRest and friends keep doing what they do best.
- **Engine support via adapters.** Adapters are external processes speaking a small JSON protocol over stdio — any language, community-extensible. The core never contains engine-specific logic.
- **Sandboxes are pluggable.** Docker containers and Kubernetes Jobs today, remote hosts later. The core only asks for "a disposable runtime".
- **Evidence is the product.** Every run appends a hash-chained, ed25519-signed record. History cannot be silently rewritten, and third parties can verify it without trusting your dashboard.

## Non-goals

Probavi will **not**: take backups, implement its own scheduler, manage database credentials beyond what a drill needs, or attempt to be a monitoring platform. Small core, sharp purpose.

## Contributing

The adapter protocol (v0) and evidence schema (v1) specs in `docs/` are normative and frozen — feedback on them is the most valuable contribution right now; open an issue. Machine-readable JSON Schemas for both live in `docs/schemas/`. Code contributions are welcome under DCO sign-off (`git commit -s`): start with `AGENTS.md` (the engineering rules this repo is held to) and the skills under `.claude/skills/`, which double as contributor guides for adapter and evidence work. New adapters can be built in any language from `docs/adapter-protocol.md` alone — that is the point of the protocol.

## Development transparency

Probavi is developed AI-assisted with human review. The guarantees do not rest on trusting any author, human or AI — they rest on the same principle the product itself sells: verifiable evidence. The specs are normative, the trust-core packages carry near-100% test coverage enforced by a CI ratchet, tamper-detection has an explicit test matrix, and every drill's proof can be re-verified offline by anyone. Don't trust; verify.

## License

[Apache-2.0](LICENSE). Probavi follows an open-core model: everything in this repository — the CLI, adapters, evidence chain, and verifier — is and stays free software. Planned organisational features (fleet dashboard, audit report exports) will be offered commercially, built on top of this core. The evidence format and the verifier will always remain freely available: proofs you can only check for a fee would not be proofs.
