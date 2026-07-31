# Probavi Adapter Protocol — v0

Status: **v0 — approved by the maintainer 2026-07-31. NORMATIVE.**
The core and all adapters implement this document. Any change requires a
version bump here before any code changes. The key words MUST, MUST NOT,
SHOULD, and MAY are to be interpreted as described in RFC 2119.

Protocol identifier: `probavi-adapter/0`.

---

## 1. Model

An adapter is an **external executable**. Any language may implement one.
The core launches a fresh adapter process **per operation**, speaks
line-delimited JSON with it over stdin/stdout, and inspects the exit code.
stderr is captured verbatim into drill logs (never parsed, never included in
evidence records).

There are exactly four operations:

| Operation     | Purpose                                                                  |
|---------------|--------------------------------------------------------------------------|
| `probe`       | Report adapter identity, protocol versions, supported source kinds, capabilities. |
| `provision`   | Given a backup source and a running sandbox, produce a serving database instance; return connection info, source identity, and timings. |
| `healthcheck` | Verify a provisioned instance is serving queries.                        |
| `teardown`    | Release everything the adapter created **outside** the sandbox. Idempotent. |

During `provision`, `healthcheck`, and `teardown` the adapter acts on the
sandbox exclusively through **core-mediated sandbox verbs** (§4): it never
talks to Docker, Kubernetes, or any provider directly, and it never needs
network access to the restored database. Engine tooling (e.g. `pg_restore`)
runs *inside* the sandbox image, so tool and server versions always match.

Division of responsibilities:

- **Core / sandbox provider**: creates the sandbox before `provision` and
  destroys it after `teardown` — unconditionally, on every failure path.
  Enforces all wall-clock timeouts. Runs validation checks (via §6.1
  `sql_runner`). Assembles and signs evidence.
- **Adapter**: everything engine-specific — interpreting the backup source,
  driving the restore, detecting *engine* readiness (the sandbox being up
  does not mean the database is ready), measuring restore-phase timings,
  computing the backup checksum.

## 2. Process contract

### 2.1 Invocation and lifetime

- The core resolves the drill config's `adapter: <name>` to an executable
  `probavi-adapter-<name>` on `PATH`, or to an explicit path if the config
  gives one. The executable is invoked with **no arguments**; all input
  arrives via stdin. Command-line arguments are reserved for future protocol
  versions.
- The core writes exactly one request message (§3.1), then keeps stdin open
  for sandbox-verb replies until the adapter's final response (§3.4) arrives,
  then closes stdin. EOF on stdin means the core is gone: the adapter MUST
  stop work and exit.
- One process per operation. State that must survive between operations is
  carried in the `state` object (§6.2) — returned by `provision`, stored by
  the core, and passed verbatim to `healthcheck` and `teardown`.

### 2.2 Framing

- Every message is one JSON object, UTF-8, on a single line terminated by
  `\n` (NDJSON). No BOM. Embedded newlines in strings MUST be escaped as
  usual JSON.
- Maximum message size: 4 MiB. Binary data crosses the protocol only
  base64-encoded and only in the fields defined for it (§4.1).
- Anything on stdout that is not a well-formed protocol message is a
  protocol violation: the core logs it and fails the operation with
  `adapter_crash`. Adapters MUST NOT let engine tools write to the adapter's
  stdout — capture or redirect to stderr.

### 2.3 Exit codes

| Exit code | Meaning |
|-----------|---------|
| 0         | A final response (§3.4) was written — including `"ok": false` error responses. |
| non-zero, or exit without a final response | Adapter crash. The core records the operation as failed with code `adapter_crash`. |

### 2.4 Signals and cancellation

- On drill timeout or user cancellation the core sends **SIGTERM**, waits a
  grace period (default 10 s, core-configurable), then **SIGKILL**.
- On SIGTERM the adapter MUST stop issuing new sandbox calls and SHOULD
  write a final response with error code `cancelled` before exiting.
- Whatever happens to `provision` — crash, SIGKILL, clean failure — the core
  MUST subsequently invoke `teardown` in a fresh process with a fresh
  deadline, and the sandbox provider MUST destroy the sandbox afterwards.
  Cleanup never depends on the fate of the operation that made the mess.

### 2.5 Environment and secrets

Secrets travel **only** in environment variables — never inside JSON
payloads, never on stdout/stderr.

The adapter process environment is allowlisted by the core:

- baseline variables (`PATH`, `HOME`, `LANG`, `TZ`),
- variables named in the drill config's `source.credential_env` list
  (credentials the adapter needs to *read* the backup source), passed
  through unchanged; their **names** are echoed in the `provision` request
  so the adapter knows what is available,
- `PROBAVI_SANDBOX_PASSWORD` — an ephemeral secret generated by the core per
  drill. If the restored engine needs an authenticated superuser, the
  adapter SHOULD set its password to this value and reference it via
  `connection.password_env` (§6.2).

Adapters MUST NOT print secret values to stderr and MUST NOT include them in
any protocol message. `error.message` and `detail` fields are shown to
humans and stored in logs: redact.

## 3. Messages

Four message shapes exist. Every message carries `protocol` and
`request_id`; the adapter MUST echo the `request_id` it received. A
`request_id` is an opaque unique string chosen by the core.

### 3.1 Request (core → adapter, first message)

```json
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "op": "provision", "payload": { }}
```

If the adapter does not support the requested `protocol`, it MUST respond
with error code `unsupported_protocol` and list the versions it supports in
`error.detail.supported`.

### 3.2 Sandbox call (adapter → core)

```json
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "sandbox_call": {"call_id": "c1", "verb": "exec", "args": { }}}
```

- Only valid during `provision`, `healthcheck`, and `teardown`. In `probe`
  it is a protocol violation.
- At most **one** outstanding call: the adapter MUST wait for the matching
  `sandbox_result` before sending the next call or the final response.
- `call_id` is an opaque string chosen by the adapter, unique within the
  operation.

### 3.3 Sandbox result (core → adapter)

```json
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "sandbox_result": {"call_id": "c1", "ok": true, "value": { }}}
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "sandbox_result": {"call_id": "c1", "ok": false, "error": {"code": "sandbox_error", "message": "…", "retryable": true}}}
```

A failed sandbox call does not abort the operation by itself: the adapter
decides whether to retry, work around, or give up with a final error.

### 3.4 Final response (adapter → core, last message)

```json
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "ok": true, "payload": { }}
{"protocol": "probavi-adapter/0", "request_id": "r-8f2c", "ok": false, "error": {"code": "source_corrupt", "message": "input file does not appear to be a valid archive", "retryable": false, "detail": { }}}
```

Exactly one final response per operation. Anything sent after it is a
protocol violation (logged and ignored by the core).

## 4. Sandbox verbs

The sandbox is already created and running when `provision` starts, but the
engine inside it may not be ready — engine readiness is the adapter's job
(poll with `exec`; beware engines whose init sequence answers probes before
the real server is up, e.g. PostgreSQL's initdb-phase temporary server).

v0 defines two verbs. New verbs (e.g. streaming transfer for very large
backups) require a protocol version bump.

### 4.1 `exec`

Run a command inside the sandbox.

Args:

| Field             | Type      | Req. | Meaning |
|-------------------|-----------|------|---------|
| `argv`            | string[]  | yes  | Command and arguments; executed directly, no shell. |
| `env`             | object    | no   | Extra environment for the command (string → string). |
| `stdin_b64`       | string    | no   | Standard input for the command, base64. |
| `timeout_seconds` | number    | no   | Per-command limit enforced by the core; on expiry the command is killed and the call fails with `timeout`. |

Value:

| Field              | Type    | Meaning |
|--------------------|---------|---------|
| `exit_code`        | integer | Command exit code. Non-zero is **not** a failed call — inspect it. |
| `stdout_b64`       | string  | Captured stdout, base64, capped at 256 KiB before encoding. |
| `stderr_b64`       | string  | Captured stderr, same cap. |
| `truncated`        | boolean | True if either capture hit the cap. |
| `duration_seconds` | number  | Measured by the core. |

### 4.2 `put_file`

Copy a file from the host into the sandbox. The core only permits source
paths that belong to the drill's configured backup source; anything else
fails the call with `invalid_request`.

Args: `source_path` (host path, string, required), `dest_path` (sandbox
path, string, required), `mode` (octal string, optional, default `"0600"`).

Value: `bytes_copied` (integer), `duration_seconds` (number).

## 5. Error model

Error object (used in final responses and sandbox results):

| Field       | Type    | Req. | Meaning |
|-------------|---------|------|---------|
| `code`      | string  | yes  | One of the registry below. |
| `message`   | string  | yes  | Human-readable, single line, no secrets. |
| `retryable` | boolean | yes  | Hint: could an identical retry plausibly succeed? The core's retry policy has the final say. |
| `detail`    | object  | no   | Machine-readable extras (e.g. `supported` protocol list). No secrets. |

Registry (v0 — extending it requires a protocol version bump):

| Code                   | Emitted by  | Meaning                                            | Typical `retryable` |
|------------------------|-------------|----------------------------------------------------|---------------------|
| `invalid_request`      | both        | Malformed message, unknown field misuse, disallowed path. | false          |
| `unsupported_protocol` | adapter     | Requested protocol version not implemented.        | false               |
| `unsupported_source`   | adapter     | Source `kind` not supported (see `probe`).         | false               |
| `source_not_found`     | adapter     | Backup source absent at the given location.        | false               |
| `source_unreadable`    | adapter     | Source exists but cannot be read (permissions, credentials). | false        |
| `source_corrupt`       | adapter     | Source read but rejected by the engine tooling.    | false               |
| `restore_failed`       | adapter     | Restore ran and failed for engine reasons. Partial restores MUST end here — never report success past ignored errors. | false |
| `engine_not_ready`     | adapter     | Engine never became ready within the adapter's readiness budget. | true  |
| `sandbox_error`        | core        | A sandbox verb could not be executed (runtime died, provider error). | true |
| `timeout`              | both        | A per-command or operation deadline expired.       | true                |
| `cancelled`            | adapter     | SIGTERM received; operation abandoned cleanly.     | true                |
| `adapter_crash`        | core (assigned) | No well-formed final response (crash, stdout pollution, non-zero exit). | false |
| `internal`             | both        | Bug; anything that fits nothing above.             | false               |

## 6. Operations

### 6.1 `probe`

No sandbox exists; no credentials are present; no sandbox calls allowed.
Request payload: `{}`.

Response payload:

```json
{
  "name": "postgres",
  "adapter_version": "0.1.0",
  "protocol_versions": ["probavi-adapter/0"],
  "engine": {"name": "postgresql"},
  "sources": [
    {"kind": "pgdump",      "capabilities": {"pitr": false}},
    {"kind": "pgdump_dir",  "capabilities": {"pitr": false}},
    {"kind": "pgbackrest",  "capabilities": {"pitr": true}}
  ],
  "sql_runner": {
    "argv": ["psql", "-U", "{{user}}", "-d", "{{database}}", "-tA", "-v", "ON_ERROR_STOP=1", "-c", "{{sql}}"],
    "env": {}
  },
  "verbs_required": ["exec", "put_file"]
}
```

- `sources[].kind` values are adapter-defined identifiers; the drill config's
  `source.kind` must match one of them.
- `sql_runner` is how the core executes validation checks **without learning
  engine concepts**: it is a declarative template, run via the `exec` verb
  inside the sandbox. Placeholders `{{user}}`, `{{database}}`, `{{sql}}`,
  `{{password}}` are substituted by the core as literal strings, one argv
  element each, no shell involved; `{{password}}` resolves to the value of
  the env var named by `connection.password_env` and may appear only in
  `sql_runner.env` values. The runner MUST print the result rows to stdout,
  one row per line, tab-separated columns, no decoration, and exit non-zero
  on SQL error.

### 6.2 `provision`

The sandbox runtime is up; the engine may still be booting.

Request payload:

```json
{
  "source": {
    "kind": "pgdump",
    "path": "/backups/orders/latest.dump",
    "params": {},
    "credential_env": ["ORDERS_BACKUP_PASSPHRASE"]
  },
  "sandbox": {"scratch_dir": "/tmp"},
  "options": {},
  "pitr": {"target_time": "2026-07-30T14:32:00Z"}
}
```

- `source.params` and `options` pass engine-specific drill-config settings
  through the core untouched (the core never interprets them).
- `pitr` is present only if the drill requests point-in-time recovery; the
  core sends it only to adapters whose `probe` declared `pitr: true` for the
  chosen source kind.
- `sandbox.scratch_dir` is a writable directory inside the sandbox
  guaranteed by the provider.

The adapter then, via sandbox verbs: waits for engine readiness, transfers
the source (`put_file`), restores, and verifies the engine still serves.

Response payload:

```json
{
  "connection": {
    "scheme": "postgresql",
    "host": "127.0.0.1",
    "port": 5432,
    "database": "postgres",
    "user": "postgres",
    "password_env": "PROBAVI_SANDBOX_PASSWORD"
  },
  "source_identity": {
    "checksum": "sha256:9f2a…",
    "size_bytes": 565248,
    "created_at": "2026-07-30T01:58:02Z"
  },
  "timings": {
    "engine_ready_seconds": 1.17,
    "transfer_seconds": 0.11,
    "restore_seconds": 0.19
  },
  "state": {"restored_database": "postgres"}
}
```

- `connection` describes reachability **from inside the sandbox** (all
  access happens via `exec`); `password_env` is optional and names an env
  var — never a value.
- `source_identity.checksum` is REQUIRED: sha256 over the source bytes. For
  multi-file sources the adapter MUST document its canonical hashing rule in
  its README. `created_at` is the backup's own creation time if derivable,
  else `null`. This feeds the evidence record's backup identity.
- `timings` MUST be real measurements (monotonic clock), not estimates:
  `engine_ready_seconds` (waiting for the engine to accept connections),
  `transfer_seconds` (moving the source into the sandbox),
  `restore_seconds` (engine restore only). The core separately measures
  sandbox provisioning and validation; together these form the evidence
  record's per-phase timing breakdown.
- `state` is opaque to the core: stored as-is, passed verbatim to
  `healthcheck` and `teardown`. No secrets (it enters logs, not evidence).

### 6.3 `healthcheck`

Request payload: `{"connection": { }, "state": { }}` — both exactly as
returned by `provision`.

Response payload:

```json
{"healthy": true, "latency_seconds": 0.02, "detail": "accepting connections; 1 database"}
```

`healthy: false` with `ok: true` is a valid outcome (the check ran; the
verdict is negative). Reserve final errors for the check itself failing.

### 6.4 `teardown`

Request payload:

```json
{"state": { }, "reason": "completed"}
```

- `reason` is one of `completed`, `failed`, `timeout`, `cancelled`.
- If `provision` crashed before returning, the core sends `state: {}` —
  teardown MUST cope with empty and partial state.
- Teardown MUST be idempotent: any number of invocations, any interleaving
  with crashes, same result.
- Sandbox destruction is the provider's job and happens regardless; this
  operation releases whatever the adapter created **outside** the sandbox
  (staging files, temporary cloud objects). Most adapters have nothing to do
  and return immediately.

Response payload: `{"released": true}`.

## 7. Timing and measurement duties

| Phase                    | Measured by | Reported in                        |
|--------------------------|-------------|-------------------------------------|
| sandbox provisioning     | core        | evidence (`timings_ms.provision`)   |
| engine readiness wait    | adapter     | `provision.timings.engine_ready_seconds` |
| source transfer          | adapter     | `provision.timings.transfer_seconds` |
| engine restore           | adapter     | `provision.timings.restore_seconds` |
| validation checks        | core        | evidence (`timings_ms.validate`)    |

Rationale: lumping sandbox startup into "restore time" makes the RTO trend
meaningless (measured in the Phase 0 PoC: 1.17 s of a 1.47 s "restore" was
container startup). Each party measures what only it can see.

## 8. Versioning

- The protocol identifier is `probavi-adapter/<major>`. Any
  backward-incompatible change — new required field, changed semantics, new
  error code, new verb — increments the major and is recorded in this
  document's changelog section.
- Adapters declare every version they speak in `probe.protocol_versions`;
  the core picks the highest common one and uses it in all messages of a
  drill.
- The protocol version is independent of the Probavi binary version and of
  each adapter's own `adapter_version`.

## 9. Worked example: a complete fake adapter

A minimal but fully conformant adapter for an imaginary `nulldb` engine, in
POSIX shell + `jq`. It demonstrates framing, one sandbox call per verb,
state, and error handling. (Illustrative; real adapters should be real
programs with tests.)

```sh
#!/bin/sh
# probavi-adapter-nulldb — fake adapter, speaks probavi-adapter/0
set -eu

send() { printf '%s\n' "$1"; }                 # stdout = protocol only
log()  { printf '%s\n' "$1" >&2; }             # stderr = logs

read -r REQ
PROTO=$(printf '%s' "$REQ" | jq -r .protocol)
RID=$(printf '%s' "$REQ" | jq -r .request_id)
OP=$(printf '%s' "$REQ" | jq -r .op)

if [ "$PROTO" != "probavi-adapter/0" ]; then
  send "$(jq -nc --arg rid "$RID" '{protocol:"probavi-adapter/0",request_id:$rid,ok:false,
    error:{code:"unsupported_protocol",message:"only probavi-adapter/0",
           retryable:false,detail:{supported:["probavi-adapter/0"]}}}')"
  exit 0
fi

# Issue one sandbox call and read its result. $1 = verb, $2 = args JSON.
sandbox() {
  send "$(jq -nc --arg rid "$RID" --arg verb "$1" --argjson args "$2" \
    '{protocol:"probavi-adapter/0",request_id:$rid,
      sandbox_call:{call_id:"c1",verb:$verb,args:$args}}')"
  read -r RES
  printf '%s' "$RES" | jq -e '.sandbox_result.ok' >/dev/null ||
    { log "sandbox call failed: $RES"; return 1; }
  printf '%s' "$RES" | jq -c '.sandbox_result.value'
}

case "$OP" in
probe)
  send "$(jq -nc --arg rid "$RID" '{protocol:"probavi-adapter/0",request_id:$rid,ok:true,
    payload:{name:"nulldb",adapter_version:"0.0.1",
      protocol_versions:["probavi-adapter/0"],engine:{name:"nulldb"},
      sources:[{kind:"nullfile",capabilities:{pitr:false}}],
      sql_runner:{argv:["cat","/dev/null"],env:{}},
      verbs_required:["exec","put_file"]}}')"
  ;;
provision)
  SRC=$(printf '%s' "$REQ" | jq -r .payload.source.path)
  [ -f "$SRC" ] || {
    send "$(jq -nc --arg rid "$RID" --arg m "no such file: $SRC" \
      '{protocol:"probavi-adapter/0",request_id:$rid,ok:false,
        error:{code:"source_not_found",message:$m,retryable:false}}')"
    exit 0; }
  T0=$(date +%s.%N)
  sandbox put_file "$(jq -nc --arg s "$SRC" '{source_path:$s,dest_path:"/tmp/n.bak"}')" >/dev/null
  T1=$(date +%s.%N)
  SUM=$(sandbox exec '{"argv":["sha256sum","/tmp/n.bak"]}' |
        jq -r .stdout_b64 | base64 -d | cut -d' ' -f1)
  T2=$(date +%s.%N)
  send "$(jq -nc --arg rid "$RID" --arg sum "sha256:$SUM" \
    --argjson tt "$(echo "$T1 $T0" | awk '{print $1-$2}')" \
    --argjson tr "$(echo "$T2 $T1" | awk '{print $1-$2}')" \
    '{protocol:"probavi-adapter/0",request_id:$rid,ok:true,payload:{
      connection:{scheme:"nulldb",host:"127.0.0.1",port:0,database:"null",user:"null"},
      source_identity:{checksum:$sum,size_bytes:0,created_at:null},
      timings:{engine_ready_seconds:0,transfer_seconds:$tt,restore_seconds:$tr},
      state:{file:"/tmp/n.bak"}}}')"
  ;;
healthcheck)
  send "$(jq -nc --arg rid "$RID" '{protocol:"probavi-adapter/0",request_id:$rid,ok:true,
    payload:{healthy:true,latency_seconds:0,detail:"nulldb is eternally healthy"}}')"
  ;;
teardown)
  send "$(jq -nc --arg rid "$RID" '{protocol:"probavi-adapter/0",request_id:$rid,ok:true,
    payload:{released:true}}')"
  ;;
*)
  send "$(jq -nc --arg rid "$RID" --arg m "unknown op: $OP" \
    '{protocol:"probavi-adapter/0",request_id:$rid,ok:false,
      error:{code:"invalid_request",message:$m,retryable:false}}')"
  ;;
esac
```

## 10. Conformance

`probavi adapter conformance <cmd>` (ROADMAP Phase 2) will assert, per
operation: framing discipline (single-line JSON, `request_id` echo, nothing
after the final response, no stdout pollution), the error registry mapping
for missing/corrupt sources, teardown idempotence including the empty-state
crash case, SIGTERM handling within grace, and that reported timings are
plausible measurements. A new adapter is "done" only when conformance
passes.

## 11. Remaining before v0 freeze

- [ ] Machine-readable JSON Schemas for all message and payload shapes
      (`docs/schemas/adapter/*.json`), generated from this document and
      verified in CI against the golden-file tests.
- [ ] Conformance suite: exact test list frozen alongside the Phase 2
      implementation.

## Changelog

- v0 (2026-07-31): initial complete draft — bidirectional core-mediated
  sandbox-verb model (`exec`, `put_file`), four operations fully specified,
  error registry, timing duties informed by the Phase 0 PoC findings.
  Reviewed and approved by the maintainer 2026-07-31; normative from this
  date. Editorial (no wire change): §7 evidence field references aligned
  with the evidence schema's `timings_ms` integer-millisecond naming.
