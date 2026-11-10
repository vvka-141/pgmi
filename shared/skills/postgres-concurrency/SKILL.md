---
name: postgres-concurrency
description: PostgreSQL recipes for concurrent writers - a work queue in a table claimed with SELECT FOR UPDATE SKIP LOCKED, advisory locks (pg_advisory_xact_lock, pg_try_advisory_lock) so only one runner works at a time, and upserts with INSERT ON CONFLICT versus MERGE. Use when building a job queue or background workers, preventing a task from running twice, serialising work per key, or writing insert-or-update logic that must hold under concurrent sessions.
---

# PostgreSQL concurrency recipes

If this project's README has a section on background jobs, follow it first.

| Facing | Read |
|---|---|
| "Several workers take jobs from one table" | [SKIP LOCKED queue](references/skip-locked-queue.md) |
| "This job must never run twice at once" | [advisory locks](references/advisory-locks.md) |
| "Insert or update by key" / "sync a table with a feed" | [upsert vs MERGE](references/upsert-vs-merge.md) |

A check-then-write in two statements races under `READ COMMITTED`. Let one
statement decide (a constraint, `ON CONFLICT`, a row or advisory lock), and
prove the rejection in a test. A test runs in one session, so it can prove
the rule, never the contention; say so when that matters.
