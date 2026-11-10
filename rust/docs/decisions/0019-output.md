# 0019: Output: notices, summary line, `--json`, logging

Status: proposed, 2026-11-10. Ticket: PGMI-829.
Checked on crates.io, 2026-11-10: serde_json 1.0.154, jiff 0.2.42 (already
in the build through google-cloud-auth), tracing 0.1.44 (used inside the
cloud crates), crossterm 0.29.0 (colours).

## Context

Notices go to stderr as they arrive, in psql's layout (NOTICE, INFO, DEBUG
bare; WARNING and above as `WARNING:  msg`; `DETAIL:  `/`HINT:  ` lines; LOG
hidden; DEBUG only with `-v`, which also prefixes the elapsed time). Each run
ends with one summary line (`ok <db>: ...` / `FAILED <db>: ...`, or `✓`/`✗`
with colour on a terminal). `--json` prints one object on stdout at the end of
every run, with the keys, null and omission rules of `docs/CLI.md`, notices
truncated to the first 50, up to 200 WARNINGs from the middle and the last
150, and every value password-redacted.

## Options

- **Logging**: `tracing` with a subscriber (structured, and would also show
  the cloud SDKs' own logs) or a small `Reporter` of pgmi's own. The cloud
  SDKs' debug logs can carry tokens and request bodies, and pgmi's stderr is
  a contract, not a log.
- **JSON**: `serde_json::Value` built by hand, or typed structs with serde
  derive (field order fixed by declaration, null versus absent explicit per
  field).

## Decision

- A `Reporter` owns stderr: one lock, one line per write, the colour and
  glyph decision of record 0012, the `-v` elapsed prefix. No tracing
  subscriber is installed, so SDK logs go nowhere, with or without `-v`.
- Notices are taken from the connection future as they arrive (record 0004).
  The label is the unlocalised severity (the protocol's `V` field), so
  `WARNING:` stays English whatever `lc_messages` is. The same notice goes
  to the Reporter and, for `--json` and `serve`, to a `NoticeLog` that keeps
  the first 50, a bounded WARNING buffer, and a ring of the last 150, and
  counts the rest for `noticesTruncated`.
- `DeployRecord` is one serde struct in the documented key order: provenance
  keys always present (`null` where the docs say), diagnostic keys
  `skip_serializing_if = "Option::is_none"`. The same struct is the
  `serve` deploy result, so the two cannot drift.
- `startedAt`/`finishedAt`: jiff, UTC, milliseconds (`...T14:02:11.402Z`).
- Redaction: one function that removes the password and token from any
  string (connection strings in every format, `password=`, the AWS
  signature), applied to `error` and to every string in the record and on
  stderr.
- The summary line and `--json` are written last, after the exit code is
  final, including after a panic or an interrupt (record 0002).

## Consequences

- Open question: the duration in the summary line. The docs show `0.41s` and
  `0.05s` (two decimals); Go's own duration format would print `53.1ms`.
  Which does the Go binary print? The cases do not pin it.

## Serves

Notices and the summary line; The --json record; Verbose output; Error
attribution; serve (deploy result); Terminal behaviour (colour).
