---
name: evidence-integrity
description: Mandatory rules for any change touching Probavi's evidence system — internal/evidence/, docs/evidence-schema.md, signing, hashing, canonicalization, key handling, the JSONL store, or the "probavi evidence verify" command. Use this skill whenever a task involves evidence records, signatures, hash chains, audit reports, tamper-evidence, or serialization of records, even indirectly (e.g. "add a field to the run result").
---

# Probavi evidence integrity

The evidence log is the product's core trust artifact: it may be shown to auditors and insurers, and its whole value is that it cannot be silently altered. Treat this code like cryptographic protocol code, because it is.

## Non-negotiable rules

- **Append-only, forever.** Never write code that mutates, reorders, or deletes existing records — including "just fixing" a malformed line. Corrections are new records referencing the old one.
- **Canonical bytes or nothing.** Hashing and signing operate on the canonical serialization defined in `docs/evidence-schema.md`. Never hash/sign the output of a generic `json.Marshal` without going through the canonicalization function; map iteration order will burn you.
- **Schema changes are spec changes.** Adding/removing/renaming any record field requires editing `docs/evidence-schema.md` first (with schema version bump and migration note), maintainer approval, then code. No exceptions for "harmless" additions.
- **Redaction is part of correctness.** Credentials, connection strings, and raw row data from checks must never enter a record. When adding data to records, ask: "would I show this line to an external auditor?"
- **Keys:** read from files with restrictive permissions; never log, never embed in config values, never include private material in records. Rotation uses overlapping `key_id`s, not in-place replacement.
- **Failed drills still produce signed records.** Do not add early-return paths that skip evidence writing on error; a crash that leaves no record is a bug of the highest severity.

## Testing requirements

- Golden-file tests: canonical bytes for representative records are committed; any diff fails CI loudly.
- Chain tests: verify detection of (a) modified record, (b) removed record, (c) reordered records, (d) forged signature with wrong key.
- Round-trip: write → read → verify must pass on every commit; `probavi evidence verify` is exercised in CI against a generated log.
- Near-100% coverage for `internal/evidence` is the target; if a branch is hard to test, redesign it.

## When in doubt

Stop and ask. A wrong decision here is a one-way door: logs already written with a flawed scheme cannot be re-signed honestly.
