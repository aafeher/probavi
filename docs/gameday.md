# Probavi DR game-day orchestration

This document is normative for the game-day configuration format, the
execution semantics of `probavi gameday`, and the machine-readable summary
it prints. A game-day exercises what single drills cannot: that an entire
service — several databases with recovery-order dependencies — comes back,
and how long the whole recovery takes.

Status: implemented 2026-08-02.

---

## 1. Purpose and principles

A **game-day** is an orchestrated set of restore drills ("members")
executed in dependency order: restore the auth database before the
application database that cannot start without it, and measure the
end-to-end wall clock of the whole exercise.

Game-days are part of the Apache-2.0 core and free forever, with no
member limit — the open-core line runs at the organisational layer
(ROADMAP.md Phase 3), never at how many databases one exercise proves.

1. **A member is exactly a drill.** Each member references a normal drill
   configuration file and runs through the identical pipeline as
   `probavi run` — sandbox, restore, checks, one signed evidence record,
   metrics, notifications. Members stay independently runnable from cron;
   the game-day adds ordering and aggregation, nothing else.
2. **The evidence schema is untouched.** The proof of a game-day is the
   member records it appended — each signed and chained in its member's
   configured log. The game-day summary (§5) is a signpost to those
   records (name + evidence path + seq), never a substitute. A signed
   session-level record is a possible future evidence-schema revision and
   would go through a spec round first (AGENTS.md §5.1).
3. **Maximum information per exercise.** A failed member skips its
   dependents (restoring an application database against a
   non-recoverable auth database proves nothing) but never stops
   independent branches: one game-day answers "what, exactly, can we
   recover today?" in a single run.

## 2. Configuration

```yaml
# gameday.yaml — a service-level recovery exercise
name: shop-stack
timeout: 2h              # hard wall clock for the whole exercise (required)
max_parallel: 1          # optional; default 1 (sequential)

members:
  - name: auth-db
    config: drills/auth.yaml         # relative to this file's directory
  - name: orders-db
    config: drills/orders.yaml
    depends_on: [auth-db]
  - name: reporting-db
    config: drills/reporting.yaml
    depends_on: [auth-db, orders-db]
```

| Key | Required | Meaning |
|---|---|---|
| `name` | yes | Identifies the exercise in the summary and logs. |
| `timeout` | yes | Hard wall-clock limit for the whole game-day. On expiry, running members are cancelled (each leaves a signed `cancelled` record) and pending members are skipped. |
| `max_parallel` | no | Maximum members running concurrently; default **1** (strictly sequential in dependency order). Restore sandboxes are resource-hungry — parallelism is a deliberate opt-in. |
| `members[].name` | yes | Game-day-local label, unique within the file. |
| `members[].config` | yes | Path to the member's drill configuration, resolved relative to the game-day file's directory. |
| `members[].depends_on` | no | Names of members whose drills must **pass** before this one starts. |

Validation is strict and fail-fast, before any sandbox exists: unknown
fields and duplicate keys are errors; dependency references must exist,
self-references and duplicate edges are errors; the dependency graph must
be acyclic; every member drill configuration must itself load and
validate. Additionally, when `max_parallel` is greater than 1, no two
members may share an evidence log: the store's single-writer lock makes
concurrent drills against one log fail, so shared logs require the
default sequential mode. This conservative rule is checked at load time.

## 3. Execution semantics

- Members start when **all** of their dependencies finished with outcome
  `pass`. Ready members start in file order, at most `max_parallel` at a
  time; with the default of 1 execution is fully deterministic.
- A member whose dependency finished with any other outcome (`fail`,
  `error`, `cancelled`, or itself skipped) is marked **`skipped`** with a
  reason naming that dependency. Skipping cascades; independent branches
  are unaffected and always run to completion.
- Each member runs the full `probavi run` pipeline under a context
  derived from the game-day's: the member's own `sandbox.timeout` still
  applies, and the game-day `timeout` caps everything. Member metrics and
  notifications fire exactly as configured in the member's drill file.
- On cancellation (signal) or game-day timeout: running members are
  cancelled through their drill contexts — each appends its signed
  `cancelled` record — and members not yet started are skipped with
  reason `game-day cancelled`.
- A skipped member runs nothing and appends **no** evidence record:
  absence of a record is correct — nothing was proven about that
  database that day.

## 4. Evidence

Member records are ordinary drill records; nothing in the evidence
schema changes and nothing game-day-specific enters a signed record.
Members may share one evidence log (sequential mode only, §2) — the
records then chain in execution order, which reads naturally in an
audit — or keep per-database logs; both are fully verifiable with
`probavi evidence verify`.

## 5. Summary output

`probavi gameday` prints one JSON line on stdout when the exercise ends:

```json
{
  "gameday": "shop-stack",
  "config_hash": "sha256:…",
  "outcome": "fail",
  "members": [
    {"name": "auth-db", "outcome": "pass", "seq": 12, "evidence_path": "/var/lib/probavi/auth.jsonl",
     "checks_passed": 3, "checks_total": 3, "restore_ms": 1840, "total_ms": 9204, "duration_ms": 9411},
    {"name": "orders-db", "outcome": "fail", "seq": 31, "evidence_path": "/var/lib/probavi/orders.jsonl",
     "checks_passed": 1, "checks_total": 4, "restore_ms": 2011, "total_ms": 11876,
     "error_code": "check_failed", "duration_ms": 12040},
    {"name": "reporting-db", "outcome": "skipped",
     "skip_reason": "dependency orders-db did not pass (fail)", "duration_ms": 0}
  ],
  "total_ms": 21532
}
```

- Member entries appear in file order and carry the same fields as the
  `probavi run` summary, plus `name`, `duration_ms` (wall clock of the
  member as observed by the orchestrator), and — for skipped members —
  `skip_reason`. (`seq`, `evidence_path`) locates the member's record for
  `probavi evidence verify`.
- `error_code` is the member record's error code, with two summary-level
  additions that describe failures *outside* a record: `evidence_lost`
  (the drill ran but its record could not be written) and `setup_error`
  (the member could not be wired — bad configuration, unresolvable
  webhook environment, unknown adapter).
- Overall `outcome`: `pass` when every member passed; `fail` when any
  member's drill failed (skipped dependents of a failure ride along under
  it); `error` when no member failed but infrastructure errors,
  cancellation, or the skips they caused left part of the exercise
  unproven.
- `total_ms` is the game-day's end-to-end wall clock — the number a DR
  plan calls the service-level recovery time.

## 6. Exit codes

| Code | Meaning |
|---|---|
| 0 | Every member passed — the service is proven recoverable. |
| 1 | At least one member's drill failed (recoverability failure). |
| 2 | No member failed, but infrastructure errors, cancellation, or the skips they caused left members unproven. |
| 3 | Usage or configuration error; nothing ran. |
| 5 | At least one member's evidence record could not be written. |

Code 5 dominates 1 and 2; a missing record is the most serious condition
a trust product can end a run with.

## 7. Scheduling

Like single drills, game-days have no built-in scheduler: cron or a
systemd timer owns the cadence (README "Running on a schedule" applies
verbatim, including the flock pattern). Game-days are heavier than
drills — a sensible cadence is weekly or monthly where single drills run
daily. Member drills that also run standalone on their own schedule keep
doing so; drills are idempotent exercises, and their records land in the
same logs either way.
