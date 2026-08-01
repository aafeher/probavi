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

**Pre-alpha, working end to end for PostgreSQL and MySQL.** `probavi run` restores real backups — logical dumps (`pg_dump`, `mysqldump`) and physical backups (pgBackRest, Percona XtraBackup) — into a disposable sandbox (Docker container or Kubernetes Job), validates them, and appends a signed evidence record; `probavi evidence verify` proves the log offline. Point-in-time recovery drills ("prove we can restore to 24 hours ago") work on pgBackRest sources, and the record carries the exact instant proven. The adapter protocol (v0) and evidence schema (v1) specs in `docs/` are normative and frozen, with machine-readable JSON Schemas in [docs/schemas/](docs/schemas/); third parties can build adapters in any language from [docs/adapter-development.md](docs/adapter-development.md) and validate them with `probavi adapter conformance` — no container runtime needed. Released as **v0.1.0**: reproducible binaries for Linux and macOS (amd64/arm64) with checksums on the [releases page](https://github.com/aafeher/probavi/releases) — pre-1.0, minor versions may break, every change is in [CHANGELOG.md](CHANGELOG.md). See [ROADMAP.md](ROADMAP.md) and [AGENTS.md](AGENTS.md).

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
- **remotehost** — for dedicated targets that cannot run containers at all: transient systemd slice + per-drill workspace over plain SSH — see [Bare-host drills over SSH](#bare-host-drills-over-ssh-remotehost) below.
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

### Remote Docker over SSH

The docker provider works unchanged against a daemon on another machine — point it there with the docker CLI's native SSH transport:

```
DOCKER_HOST=ssh://drill@drill-box.internal  probavi run --config /etc/probavi/orders.yaml
```

Drills then run on the remote machine's resources while backups and evidence stay on the host that invokes `probavi`: `put_file` streams the backup bytes through the SSH connection (never a published port), and every container guarantee above — engine image version matching, `--network none`, resource caps, forced destruction — holds exactly as locally. Requirements: key-based SSH to the target and a docker daemon + CLI there. Several drill hosts may safely share one daemon: sandboxes carry a host-scoped label and each host's orphan sweep only ever touches its own containers (upgrade all sharing hosts to ≥ the version that introduced the label before pointing them at a common daemon). The SSH endpoint deliberately lives in the environment, not in the drill config — sandbox params are recorded verbatim in signed evidence records, and connection details never belong there. Residual risk to weigh: the remote daemon is effectively root on its machine, and the restored production data briefly lives on that machine's disks — give drills a machine you trust as much as the backup storage itself.

### Bare-host drills over SSH (remotehost)

When the target machine cannot run a container runtime at all (appliance-like DB hosts, restrictive policies, niche platforms), the **remotehost** provider runs drills there with nothing but OpenSSH and systemd: one sandbox is one transient systemd slice plus one per-drill workspace directory, and every command — including the engine the adapter starts — runs as a transient unit inside that slice, so resource caps bound the whole sandbox and stopping the slice kills the entire process tree. The design (and what deliberately does *not* survive without containers) is specified in [`docs/sandbox-bare-host.md`](docs/sandbox-bare-host.md). If the target *can* run Docker, prefer Remote Docker over SSH above — every container guarantee holds there.

```yaml
sandbox:
  provider: remotehost
  params:
    workspace_root: /var/lib/probavi-drills   # default
    memory: 2G                                # slice MemoryMax
    cpus: "2"                                 # slice CPUQuota (200%)
  timeout: 30m
```

```
PROBAVI_SSH_TARGET=drill@drill-box.internal  probavi run --config /etc/probavi/orders.yaml
```

**The non-negotiable premise: the target host is dedicated to drills.** It runs no other database, serves no other tenant, and holds nothing you would mind a restored production copy briefly living next to.

Target requirements — the provider probes and refuses what it can check, the rest is operator duty:

- systemd ≥ 244 as PID 1 (probed at first contact; older targets are refused).
- The engine toolchain installed **at versions matching the backups under test** — with no container image to pin versions, keeping them aligned is on you; a mismatch surfaces as an honest failed drill, never a silent wrong-version pass.
- A dedicated OS user for drills, key-based SSH only (the provider never weakens host key verification and never prompts). Root is not required: grant the drill user transient-unit rights with this polkit rule (as root, drop it into `/etc/polkit-1/rules.d/50-probavi-drill.rules`, adjusting the user name):

  ```js
  polkit.addRule(function(action, subject) {
      if (action.id == "org.freedesktop.systemd1.manage-units" &&
          subject.user == "probavi-drill") {
          return polkit.Result.YES;
      }
  });
  ```

  Understand what this grants: `manage-units` lets the drill user start and stop arbitrary system units — on a shared machine that is root-equivalent, which is one more reason the dedicated-host premise is not advisory. (`systemd-run --user` was rejected as the default because per-user managers cannot enforce `MemoryMax` on some cgroup setups, and resource caps that silently do not bind have no place in a trust product.)
- Create `workspace_root` owned by the drill user (`install -d -o probavi-drill -g probavi-drill /var/lib/probavi-drills`). If several drill hosts share one target, connect them as the **same** drill user — the host-scoped orphan sweep reads owner markers inside 0700 workspaces.

Cleanup is layered: `Destroy` stops the slice and removes the workspace on every drill outcome; the next drill from the same host sweeps workspaces whose owner process died; and a target-side transient timer armed at create stops the slice and removes the workspace after a hard deadline (2 h) even if the drill host never comes back. `PROBAVI_SSH_TARGET` lives in the environment for the same reason as `DOCKER_HOST` above — connection details never enter drill config or evidence records.

Residual risk to document for yourself: restored engines listen on unix sockets in the workspace (loopback TCP only as engine-specific fallback), but there is no network namespace — host-level firewalling stays your job; command lines (including per-exec environment) are visible in the target's process list for their duration; and the workspace is deleted, not shredded — restored production data touches the target's persistent disks, so put the workspace on tmpfs or use full-disk encryption on the target if that matters to you.

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
