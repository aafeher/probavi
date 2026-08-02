# Probavi notifications — webhook delivery and payload schema

**Payload schema version: `probavi-notification/1`** (versioned independently
of the Probavi binary, like the adapter protocol and the evidence schema).
This document is normative for the `notify` drill-config section, the
delivery behavior of `probavi run`, and the payload receivers parse. The
machine-readable payload schema lives in
[`schemas/notification/payload.json`](schemas/notification/payload.json);
on any disagreement this document wins.

Status: implemented 2026-08-02.

---

## 1. Purpose and principles

A notification tells a human or a machine that a drill finished and how it
ended. Three principles bound the design:

1. **Observability, not evidence.** The signed evidence record is the
   product; the notification is a signpost pointing at it (`drill.name` +
   `seq` locate the record in the log). A notification is never a
   substitute for the record, and a delivery failure is loud (logged as an
   error) but never changes the drill's outcome or exit code.
2. **Push only where the operator points.** Notifications go exclusively
   to URLs the operator configured. There is no default endpoint, no
   phone-home, no telemetry (AGENTS.md §3.3).
3. **Webhook first.** The only channel is an HTTP(S) POST with a JSON
   body. Slack, email, and paging integrations are *recipes* on top of it
   (§7), not built-in channels.

## 2. Configuration

```yaml
notify:
  webhooks:
    - url_env: PROBAVI_WEBHOOK_URL     # URL read from this env var
      secret_env: PROBAVI_WEBHOOK_SECRET
      on: [fail, error]                # deliver only for these outcomes
    - url: https://alertmanager.internal:9093/probavi   # non-secret literal
```

Per webhook:

| Key | Required | Meaning |
|---|---|---|
| `url` / `url_env` | exactly one | The destination. `url` is a literal absolute `http(s)` URL; `url_env` names an environment variable holding one. **Token-bearing URLs (Slack, healthchecks.io, PagerDuty…) are credentials and must use `url_env`** — credentials never live in config values (AGENTS.md §3.3). |
| `secret_env` | no | Names an environment variable holding the HMAC secret for payload signing (§6). Absent means unsigned. |
| `on` | no | Subset of `pass`, `fail`, `error`, `cancelled`. Default: **all four** — silence then means the drill did not run, which makes dead-man's-switch receivers work (§7). |

Environment variables are resolved once, at drill start, before the
sandbox is created; an unset or empty variable is a configuration error
that aborts the run (exit 3). This fails fast instead of discovering a
missing variable after a long restore.

**Redaction rules (binding).** A URL that came from the environment is a
credential: no URL — from either source — is ever written to logs,
evidence records, or error messages. Logs and errors identify a webhook
only by its zero-based config index (`webhook[0]`). Transport errors are
unwrapped from Go's `*url.Error` (which embeds the full URL) before
logging; the underlying dial/DNS error may still name the target *host*,
never the path or query where tokens live.

## 3. Trigger and timing

- Exactly one event exists in schema version 1: **`drill.completed`**. It
  fires once per `probavi run`, after the evidence record has been signed
  and appended (and after metrics exposition). One POST goes to each
  configured webhook whose `on` filter matches the record's outcome, in
  config order, sequentially.
- If the drill fails so severely that **no evidence record exists** (exit
  5, evidence lost), nothing is delivered — there is no record to point
  at. Monitor the process exit code or use a dead-man's-switch recipe
  (§7); with the default `on`, absence of any notification is itself the
  alarm signal.
- Delivery runs under its **own time budget, independent of the drill
  timeout**: total 60 s across all webhooks, 10 s per attempt, 3 attempts
  per webhook with 1 s / 2 s backoff. A cancelled drill (Ctrl-C, SIGTERM)
  still notifies — the cancellation record was signed and stored, and the
  delivery context is deliberately not derived from the drill's.
- Retries happen on transport errors and 5xx responses only. Any 2xx
  response is success; any other response — including redirects, which
  are **never followed** (a redirect could leak a token-bearing URL or
  signed body to an unintended host) — is a permanent failure for this
  run. Delivery is at-most-once per webhook per run: receivers that must
  deduplicate can key on (`drill.name`, `seq`).

## 4. Request

```
POST <url> HTTP/1.1
Content-Type: application/json
User-Agent: probavi/<version>
X-Probavi-Event: drill.completed
X-Probavi-Signature-256: sha256=<hex>        (only when secret_env is set)

<payload JSON, single line>
```

## 5. Payload (`probavi-notification/1`)

```json
{
  "schema": "probavi-notification/1",
  "event": "drill.completed",
  "ts": "2026-08-01T03:10:02.481Z",
  "drill": { "name": "prod-orders-db", "config_hash": "sha256:…" },
  "adapter": "postgres",
  "outcome": "pass",
  "seq": 7,
  "checks_passed": 3,
  "checks_total": 3,
  "timings_ms": { "restore": 190, "total": 2412 },
  "error": null,
  "probavi_version": "0.1.0"
}
```

Every field is always present; nullable values are `null`, never omitted
(the evidence schema's determinism discipline). All values are copied
from the evidence record the notification announces:

| Field | Type | Source in the record |
|---|---|---|
| `schema` | const `probavi-notification/1` | — |
| `event` | const `drill.completed` | — |
| `ts` | RFC 3339 UTC, millisecond precision | `ts` |
| `drill.name`, `drill.config_hash` | string, `sha256:<hex>` | `drill` |
| `adapter` | string | `adapter.name` |
| `outcome` | `pass` \| `fail` \| `error` \| `cancelled` | `outcome` |
| `seq` | integer ≥ 1 | `seq` — locates the record for `probavi evidence verify` |
| `checks_passed`, `checks_total` | integer ≥ 0 | derived from `checks` |
| `timings_ms.restore`, `timings_ms.total` | integer ms or `null` | `timings_ms` |
| `error` | `null` or `{code, message}` | `error` — `message` is already redacted by evidence rules |
| `probavi_version` | string | `env.probavi_version` |

The payload deliberately carries **no** connection details, sandbox
parameters, credentials, or check result values — receivers are outside
the trust boundary, and everything auditable lives in the signed record.

Versioning: additive, receiver-visible changes (new fields) bump to
`probavi-notification/2`; receivers should tolerate unknown *versions*,
not unknown fields within a version.

## 6. Authenticity (HMAC signing)

When `secret_env` is set, the request carries

```
X-Probavi-Signature-256: sha256=<lowercase hex of HMAC-SHA256(secret, body)>
```

computed over the exact request body bytes. Receivers verify by
recomputing and comparing **constant-time** (e.g. Go's `hmac.Equal`).
The scheme and header shape follow the widely implemented GitHub webhook
convention. Signing proves origin and integrity; it does not prevent
replay — receivers that care should deduplicate on (`drill.name`, `seq`).
The secret is read from the environment at drill start and is never
logged.

## 7. Recipes (informative)

- **Dead-man's switch (healthchecks.io and similar).** Two webhooks on
  the same check UUID: `on: [pass]` → the ping URL, `on: [fail, error,
  cancelled]` → the same URL with `/fail` appended. The service alarms
  both on explicit failure *and* on silence — which also covers the
  no-record case of §3. Ping URLs contain the check UUID, i.e. a
  credential: use `url_env`.
- **Slack.** Slack's classic incoming webhooks require a `{"text": …}`
  shape and will reject this payload; use a **Workflow Builder** webhook
  trigger instead, which accepts arbitrary JSON and maps fields
  (`outcome`, `drill.name`, `seq`) into a message. Webhook URLs embed a
  token: `url_env`.
- **Email / anything else.** Point the webhook at a self-hosted bridge
  (e.g. an [Apprise](https://github.com/caronc/apprise) API container or
  an n8n flow) that fans out to SMTP, Teams, ntfy, etc. Verify the HMAC
  header at the bridge if it is reachable by others.

## 8. Security considerations

- Webhook URLs are commonly capability tokens; treat them as credentials
  (`url_env`, redaction rules of §2). Prefer `https` endpoints; `http`
  is accepted for air-gapped/internal receivers but exposes the payload
  and any URL token on the wire.
- The payload is metadata about a drill, not data from the restored
  database. Nothing in it is derived from database contents beyond what
  the evidence record already exposes (check names and pass/fail bits).
- Receivers are untrusted: nothing they return influences Probavi, and
  their responses are read only for the status code (bodies are drained
  and discarded, capped at 4 KiB).
