# Probavi Evidence Schema — v1

Status: **v1 — approved by the maintainer 2026-08-01; FROZEN 2026-08-01.
NORMATIVE.** The evidence format is the product's core trust artifact; treat
every field and byte here as a public API. Any change requires a schema
version bump in this document before any code changes. The key words MUST,
MUST NOT, SHOULD, and MAY are to be interpreted as described in RFC 2119.
A machine-readable JSON Schema covering every published version lives at
`docs/schemas/evidence/record.json` (derived from this document; on any
disagreement this document wins).

Schema identifier: `probavi-evidence/1`. Writers emit v1; verifiers MUST
also accept records declaring `probavi-evidence/0` (§10).

---

## 1. Goals and threat model

- A third party holding only (a) the log file and (b) the signer's public
  key(s) MUST be able to verify **offline** that: every record is authentic,
  the sequence is complete and ordered, and nothing was altered or removed.
- Records are self-describing enough to reconstruct *what was proven*: which
  backup, restored how, checked how, with what results and measured
  durations.
- Assumed attacker: someone with write access to the log file who wants to
  forge "everything was fine" (or hide a failure) after the fact —
  including the operator. Defenses: per-record ed25519 signatures over
  canonical bytes, a SHA-256 hash chain, and an append-only writer.
- Out of scope for v0: confidentiality of the log (it is designed to be
  shareable — see redaction, §8), and proving *absence* of additional logs.

## 2. The log

- A log file is UTF-8 JSONL: one record per line, terminated by `\n`.
- Each stored line IS the canonical serialization (§4) of its record —
  byte-for-byte. Pretty-printing, re-ordering, or re-encoding a stored line
  destroys verifiability by construction.
- One file = one hash chain. Multiple drills MAY share a file; their records
  interleave in append order. `seq` is per-file.
- **Append-only, forever.** No code path may mutate, reorder, or delete
  existing bytes. Corrections are new records; retention/compaction, if ever
  needed, is a future spec-level design task.
- Single writer at a time: the writer takes an advisory lock on
  `<path>.lock`, opens the log with `O_APPEND`, writes the full line, and
  fsyncs before releasing the lock.
- Torn tail (crash mid-write): on open, if the file's final bytes are not a
  complete `\n`-terminated line, the writer MUST NOT rewrite or truncate
  them; it appends a single `\n` to close the fragment (a pure append),
  logs a warning, and chains the next record from the last *valid* record.
  Verification (§9) reports such fragments as damage, distinct from
  tampering.

## 3. Record shape

Every record has exactly the fields below (fixed shape per schema version;
unknown/unavailable values are `null`, never omitted). All numbers are
integers (§4). Example — one record, shown pretty-printed for readability
only:

```json
{
  "schema": "probavi-evidence/0",
  "seq": 1042,
  "prev_hash": "sha256:b5bb9d8014a0f9b1d61e21e796d78dccdf1352f23cd32812f4850b878ae4944c",
  "ts": "2026-07-31T02:00:11.482Z",
  "drill": {
    "name": "prod-orders-db",
    "config_hash": "sha256:7d865e959b2466918c9863afca942d0fb89d7c9ac0c99bafc3749504ded97730",
    "pitr_target": null
  },
  "backup": {
    "kind": "pgdump",
    "checksum": "sha256:9f2a11a6a9e1a76f7e4c62b9b2b0a3f2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6",
    "size_bytes": 565248,
    "created_at": "2026-07-30T01:58:02.000Z"
  },
  "adapter": {"name": "postgres", "version": "0.1.0", "protocol": "probavi-adapter/0"},
  "sandbox": {"provider": "docker", "params": {"image": "postgres:16", "memory": "2GiB"}},
  "timings_ms": {
    "provision": 1170,
    "engine_ready": 1166,
    "transfer": 110,
    "restore": 190,
    "validate": 61,
    "total": 2840
  },
  "checks": [
    {"name": "service_healthy", "ok": true, "detail": "accepting connections"},
    {"name": "row_count:orders", "ok": true, "detail": "100000 rows (min 100000)"}
  ],
  "outcome": "pass",
  "error": null,
  "env": {
    "probavi_version": "0.1.0",
    "os": "linux",
    "arch": "amd64",
    "host_id": "3f7a9c2e5b1d8e04"
  },
  "sig": {
    "alg": "ed25519",
    "key_id": "a1b2c3d4e5f60718",
    "sig_b64": "hVb0(…88 base64 chars encoding the 64-byte signature…)Cg=="
  }
}
```

Field reference:

| Field | Type | Nullable | Meaning |
|-------|------|----------|---------|
| `schema` | string | no | Schema identifier, `probavi-evidence/<major>`. |
| `seq` | integer | no | 1-based, strictly consecutive per file. |
| `prev_hash` | string | no | `sha256:<64 lowercase hex>` of the previous stored line (§5); genesis: 64 zeros. |
| `ts` | string | no | Record creation time, RFC 3339 UTC, exactly millisecond precision, `Z` suffix. |
| `drill.name` | string | no | Drill identity from config. |
| `drill.config_hash` | string | no | `sha256:` over the exact drill-config file bytes as read. Proves which config ran without embedding its contents. |
| `drill.pitr_target` | string | yes | The resolved point-in-time recovery target the drill demanded of the adapter (RFC 3339 UTC, ms, `Z`). Null when the drill did not request PITR. The config may express the target relatively (e.g. "24h ago" — pinned by `config_hash`); this field records the absolute instant actually requested, which is what the record proves recoverability *to*. |
| `backup.kind` | string | no | Source kind (adapter-defined, from config). |
| `backup.checksum` | string | yes | `sha256:` over source bytes, from the adapter's `source_identity`. Null if provisioning never got that far. |
| `backup.size_bytes` | integer | yes | Source size. |
| `backup.created_at` | string | yes | Backup's own creation time if derivable (RFC 3339 UTC, ms, `Z`). |
| `adapter.name` / `.version` / `.protocol` | string | version: yes | Adapter identity; protocol version actually spoken. |
| `sandbox.provider` | string | no | Provider name (`docker`, …). |
| `sandbox.params` | object (string→string) | no | Provider parameters from config, values as written. Never tokens/handles. |
| `timings_ms.*` | integer | yes (per phase) | Per-phase durations in milliseconds (§3.1). Phases that never ran are null. |
| `checks[]` | array | no (may be empty) | Executed checks in execution order. |
| `checks[].name` | string | no | Builtin: `<builtin>[:<target>]`; custom SQL: `sql:<user-given name or index>`. Never the SQL text. |
| `checks[].ok` | boolean | no | Verdict. |
| `checks[].detail` | string | yes | Single line, ≤ 256 chars, aggregates only (§8). |
| `outcome` | string | no | `pass` \| `fail` \| `error` \| `cancelled` (§7). |
| `error` | object | yes | Null on `pass`; else `{"code": …, "message": …}` — code from the adapter-protocol registry or a check failure; message redacted, single line, ≤ 512 chars. |
| `env.probavi_version` | string | no | Core version. |
| `env.os` / `env.arch` | string | no | Runtime platform. |
| `env.host_id` | string | no | First 16 hex chars of SHA-256 of the hostname. The raw hostname MUST NOT appear (v0). |
| `sig.alg` | string | no | `ed25519`. |
| `sig.key_id` | string | no | First 16 hex chars of SHA-256 of the 32-byte public key. |
| `sig.sig_b64` | string | no | RFC 4648 base64 (with padding) of the 64-byte signature (§6). |

### 3.1 Timings

All durations are integer milliseconds, converted from measured values by
rounding half away from zero. Sources (see adapter protocol §7):

| Field | Measured by | Covers |
|-------|-------------|--------|
| `provision` | core | Sandbox creation until the runtime is up. |
| `engine_ready` | adapter | Waiting for the engine inside the sandbox to accept connections. |
| `transfer` | adapter | Moving the backup source into the sandbox. |
| `restore` | adapter | The engine restore itself. This is the headline RTO-trend number. |
| `validate` | core | Running all checks. |
| `total` | core | Drill start to verdict (excludes evidence writing). |

## 4. Canonicalization

Canonical serialization is **RFC 8785 (JSON Canonicalization Scheme, JCS)**
with one schema-level restriction:

> **Integer-only numbers.** Every number in a record MUST be an integer `n`
> with `|n| ≤ 2^53 − 1`. Fractional values, exponent notation, `-0`, `NaN`,
> and `Infinity` never occur (durations are integer milliseconds, sizes are
> integer bytes).

Consequences an implementer may rely on:

- JCS number serialization degenerates to plain decimal integers with an
  optional leading `-`; the ES6 shortest-round-trip float algorithm is never
  exercised.
- A serializer that (a) sorts object keys, (b) emits no insignificant
  whitespace, (c) escapes strings per RFC 8785 §3.2.2.2 (minimal escaping),
  and (d) outputs UTF-8 produces byte-identical results to any conforming
  JCS library for valid records.
- Key ordering follows RFC 8785: property names sorted by UTF-16 code
  units. All schema-defined keys are ASCII; `sandbox.params` keys come from
  user config and MUST be compared per the RFC rule.

Writers MUST reject (refuse to sign) any record that violates the integer
restriction, contains invalid UTF-8 in any string (serializers commonly
substitute U+FFFD silently, which would alter content before signing), or
exceeds 64 KiB canonical size. Verifiers MUST check the integer restriction
and the size limit; invalid UTF-8 in stored bytes surfaces as a
canonical-form mismatch.

## 5. Hash chain

- `record_hash(n)` = SHA-256 over the exact stored line bytes of record *n*
  — the canonical serialization **including** `sig` — excluding the
  trailing `\n`.
- `prev_hash` of record *n+1* = `"sha256:" + lowercase_hex(record_hash(n))`,
  where *n* is the previous **valid** record (§2 torn-tail rule).
- Genesis: the first record has `seq = 1` and
  `prev_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"`.
- `seq` increments by exactly 1 per valid record.

Chaining over the *signed* line (not the pre-signature bytes) makes
signature substitution a chain break as well.

## 6. Signing and keys

- Algorithm: Ed25519 (RFC 8032, "pure" — no pre-hash).
- Signed message: the canonical serialization of the record **with the
  `sig` field removed entirely** (not null — absent).
- `sig.sig_b64`: the 64-byte signature, RFC 4648 standard base64 with
  padding.
- Private key file: the 32-byte seed as 64 lowercase hex chars, single
  line, `\n`-terminated. The writer MUST refuse keys whose file mode allows
  group/other access, MUST never log key material, and MUST NOT accept keys
  from config values or environment variables — file path only.
- Public key file: 64 lowercase hex chars of the 32-byte public key.
  `key_id` = first 16 hex chars of SHA-256 over the 32 raw public-key
  bytes.
- `probavi evidence keygen` (Phase 1 CLI) generates the pair and prints the
  `key_id`.
- Rotation: begin signing with a new key at any time; old records remain
  valid under the old key. Verifiers accept a **keyring** (one or more
  public keys) and select by `key_id`. Re-signing existing records is
  forbidden — there is no honest way to do it.

## 7. Outcomes and failure records

**Every started drill MUST end in exactly one appended, signed record** —
including crashes, timeouts, and cancellations. An early-return path that
skips evidence writing is a bug of the highest severity. The core builds the
record incrementally during the drill; on any abort it fills what it knows,
nulls the rest, and signs.

| `outcome` | Meaning | Typical `error.code` |
|-----------|---------|----------------------|
| `pass` | Restore succeeded and every check passed. The backup is proven restorable. | — (`error` is null) |
| `fail` | The drill reached a verdict and the verdict is negative: the backup or restore is the problem. | `source_not_found`, `source_unreadable`, `source_corrupt`, `restore_failed`, or `check_failed` (one or more checks false) |
| `error` | Infrastructure prevented a verdict; says nothing about the backup. | `sandbox_error`, `adapter_crash`, `timeout`, `engine_not_ready`, `invalid_request`, `internal` |
| `cancelled` | Operator or signal aborted the drill. | `cancelled` |

`check_failed` is defined by this schema (it is not an adapter code). For
reporting: `fail` is a recoverability red flag; `error` is an operational
red flag; both trends matter, and conflating them would poison the audit
story.

## 8. Redaction rules

A record must be shareable with an external auditor as-is. The following
MUST NOT appear anywhere in a record:

- credential values, key material, or the *names* of credential env vars;
- connection details (hosts, ports, users, connection strings, sandbox
  tokens or handles);
- SQL text of custom checks (the check *name* identifies it; `config_hash`
  pins its definition);
- result rows or any per-row data — `checks[].detail` carries aggregates
  only (counts, ages, latencies);
- raw hostnames (§3 `env.host_id`), file paths outside `drill.config_hash`
  scope, or adapter stderr content.

The core passes every adapter-originated string destined for a record
(error messages, check details) through a redactor that masks the values of
all secrets it holds; truncation limits (§3) apply after redaction.

## 9. Verification

`probavi evidence verify --log <file> --key <pub> [--key <pub>…]`
implements exactly this algorithm; independent implementations need nothing
else. That claim is not left as an assertion: `spec/evidence` is a second
implementation written from this document alone (§12).

```text
expected_prev ← "sha256:" + 64×"0"
expected_seq  ← 1
damage        ← []

for each line L (bytes between \n terminators, in file order):
    if L is not parseable as a JSON object:
        damage.append(line_number)            # torn tail fragment (§2)
        continue                              # chain does not advance
    R ← parse(L)
    assert R.schema is a supported version                 else INVALID
    assert canonical(R) == L (byte-for-byte)               else INVALID
    assert every number in R is an integer, |n| ≤ 2^53−1   else INVALID
    assert R.seq == expected_seq                           else INVALID
    assert R.prev_hash == expected_prev                    else INVALID
    K ← keyring[R.sig.key_id]                              else INVALID (unknown key)
    M ← canonical(R without sig)
    assert ed25519_verify(K, M, base64_decode(R.sig.sig_b64)) else INVALID
    expected_prev ← "sha256:" + hex(sha256(L))
    expected_seq  ← expected_seq + 1

result: INVALID on first assertion failure (report line + reason)
        VALID_WITH_DAMAGE if damage nonempty (report damaged lines)
        VALID otherwise
```

Exit codes: `0` VALID, `1` VALID_WITH_DAMAGE, `2` INVALID.

Security note: an unparseable line can only ever be a crash artifact —
signed content cannot be altered or removed this way. Modifying any stored
line fails the canonical-bytes or signature check; deleting a line breaks
`seq`/`prev_hash` continuity; reordering breaks `prev_hash`. Appending
garbage is detected and reported but forges nothing.

## 10. Versioning and migration

- The schema identifier is `probavi-evidence/<major>`. **Any** field
  addition, removal, rename, type change, or semantic change increments the
  major and adds a migration note here.
- A log file MAY contain records of different schema versions (an upgrade
  happened mid-file); each record is validated against its own declared
  version. Verifiers MUST support every published version.
- Existing records are never rewritten to a new version.

Published versions and migration notes:

| Version | Shape difference | Migration |
|---------|------------------|-----------|
| `probavi-evidence/0` | v1 without `drill.pitr_target`. | None — v0 records lack the field entirely (fixed shape per version) and remain valid forever under v0. Writers emit v1 from 2026-08-01. |
| `probavi-evidence/1` | Current (§3). | — |

## 11. v1 freeze

**v1 is frozen as of 2026-08-01** — every item below is complete. Any
further change to this schema is a version bump (§10).

- [x] Machine-readable JSON Schema (`docs/schemas/evidence/record.json`),
      covering both published versions, verified in CI against the
      golden-file tests plus mutation samples (`internal/spec`).
      Done 2026-08-01.
- [x] Worked example: byte-exact 3-record logs for both published versions
      (`docs/schemas/evidence/examples/log_v0.jsonl`, `log_v1.jsonl`) with
      the signer's public key committed alongside (`examples/signer.pub`;
      the key pair is the deterministic test key with seed bytes 0x00…0x1f).
      CI verifies both logs offline with only the committed public key.
      Done 2026-08-01; moved out of `internal/` and published as conformance
      vectors 2026-08-02 (§12).

## 12. Independent verification

This section is normative about *availability*, not about the format: it
records a standing commitment, and nothing here changes a byte of §1–§11.

Verification of the evidence format is permanently free and permanently
independent of the Probavi product:

- **The specification is this document**, versioned as
  `probavi-evidence/<major>` on its own cadence, independent of the Probavi
  binary's version. The machine-readable JSON Schema for every published
  version lives at `docs/schemas/evidence/record.json`.
- **The conformance vectors are published**, not internal test fixtures:
  `docs/schemas/evidence/examples/` holds byte-frozen logs for every
  published schema version plus the public key that signed them. The signing
  key is the deterministic test key and is published deliberately, so anyone
  can reproduce the logs. It is a test vector and MUST NOT be used
  operationally.
- **A reference verifier ships as a standalone tool**, `spec/evidence`, a
  separate Go module with no dependencies and no code shared with the
  Probavi core. Being a separate module, it *cannot* import
  `internal/evidence` — the independence is enforced by the Go toolchain
  rather than by convention. It was written from this document alone.

The two implementations are held together by the published examples rather
than by shared code: the core's tests pin that it emits exactly those bytes,
and the verifier's tests pin that an implementation which has never seen the
core accepts exactly those bytes. A divergence turns one of the two suites
red. This is the same technique the adapter protocol uses in `internal/conformance`,
applied to the format that carries the product's actual claim.

A third-party implementation is expected to need nothing beyond this
document; where the reference verifier found the text worth re-reading, the
notes are collected in `spec/evidence/README.md`.

## Changelog

- Editorial (2026-08-02, no format change): §12 added, recording that
  verification is permanently free and independently implemented. The worked
  example moved from `internal/evidence/testdata/` to
  `docs/schemas/evidence/examples/` so that it is reachable as a published
  conformance vector — the bytes are unchanged and CI proves it. No schema
  version bump: no field, no serialization rule and no record byte is
  affected.
- v1 (2026-08-01): added `drill.pitr_target` — nullable, the resolved
  absolute point-in-time recovery target of PITR drills. Rationale: a PITR
  drill's compliance claim is "restorable *to instant T*"; without T in the
  signed record the claim would rest on unsigned logs. Approved by the
  maintainer 2026-08-01. No other shape or byte-level change; v0 records
  remain valid under v0 (§10). Addendum (same day, no byte-level change):
  machine-readable JSON Schema added at `docs/schemas/evidence/record.json`
  and the worked-example public key committed — §11 complete, **v1 frozen**.
- v0 (2026-07-31): initial complete draft. Canonicalization decided:
  RFC 8785 JCS restricted to integer-only numbers (maintainer decision
  2026-07-31). Per-phase integer-millisecond timings aligned with adapter
  protocol §7; outcome taxonomy separates recoverability failures from
  infrastructure errors; torn-tail damage semantics defined. Reviewed and
  approved by the maintainer 2026-07-31; normative from this date.
  Editorial clarification (same day, implementation finding): writers MUST
  also reject invalid UTF-8 — common serializers substitute U+FFFD
  silently, which would alter content before signing.
