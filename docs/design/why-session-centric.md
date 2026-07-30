---
title: "Why session-centric"
description: "Design record: why pgmi does all deployment work inside one PostgreSQL session, and what was rejected to get there."
weight: 20
---

# Design Record: Why Session-Centric

> **Status: FOUNDATIONAL** — this decision predates and shapes every other
> pgmi design. The mechanics it produces are documented in the
> [Session API](../session-api.md); this page records the *why*.

## Decision

All deployment work happens inside **one PostgreSQL session**. pgmi loads the
project — files, checksums, parameters, metadata — into session-scoped
temporary tables, exposes them through stable views, and then hands control
to the project's own `deploy.sql`:

![The pgmi model: pgmi prepares one PostgreSQL session and hands control to your deploy.sql](../diagrams/d01-the-pgmi-model.drawio.svg)

## Forces

- Deployment state should be **queryable by the thing doing the deploying**.
  A deploy script that can `SELECT` its own plan, parameters, and file
  contents can implement any policy — ordering, gating, idempotency — as
  ordinary SQL.
- Transactional control belongs to whoever owns the connection. One session
  means the project's SQL can wrap the entire deployment in one transaction
  and gate `COMMIT` on its own tests.
- State that exists only inside the session cannot drift, leak, or need
  cleanup: when the session ends, the temp tables are gone.

## Consequences

**Gained**: all-or-nothing deployments as a *choice* expressed in SQL;
[test-gated commits](../TESTING.md); a
[plan that is a query result](../METADATA.md#execution-order-sort-keys);
zero persistent pgmi state in the target database.

**Paid for** (recorded honestly in [Trade-offs](../TRADEOFFS.md)):
[session-mode connection requirements](../TRADEOFFS.md#connection-poolers-are-incompatible)
— transaction-mode poolers destroy `pg_temp` between statements — and
[practical file-loading limits](../TRADEOFFS.md#file-loading-has-practical-limits),
because everything rides through one session.

## Rejected alternatives

- **A tool-owned migration/version table.** The dominant industry design
  (Flyway's `flyway_schema_history` and kin) makes tracking state a default
  you inherit, with the tool's semantics baked in. pgmi rejected it: tracking
  is [a choice you make](../TRADEOFFS.md#migration-tracking-is-a-choice-you-make-not-a-default-you-inherit),
  implemented in your schema if you want it (the
  [advanced template](../advanced/_index.md) shows a complete implementation).
- **State files outside the database.** Plan files, lock files, and journals
  on disk create a second source of truth that can disagree with the
  database. Everything pgmi knows during a deployment lives in the session,
  inspectable with `SELECT`.
- **Multi-connection orchestration.** Parallel connections would break the
  single-transaction guarantee that makes test-gated deployment possible.
  Where statement-level connection semantics are genuinely needed
  (`CREATE INDEX CONCURRENTLY`), pgmi keeps one connection and changes the
  *message framing* instead — the
  [atomic-head / psql-mode-tail contract](../DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract),
  declared entirely by the SQL file itself.

## See also

- [Why pgmi](../WHY-PGMI.md) — the reader-facing case
- [Why execution fabric](why-execution-fabric.md) — the sibling decision
