---
name: postgres-indexing
description: PostgreSQL recipes for choosing, adding and reviewing indexes. Covers indexing foreign key columns and finding unindexed foreign keys, covering indexes with INCLUDE for index-only scans, expression indexes such as lower(email) or a jsonb key, BRIN indexes for large append-only time-ordered tables, and CREATE INDEX CONCURRENTLY on a live table in a pgmi deploy, including invalid indexes left by a failed build. Use when a query is slow, a delete or cascade takes too long, an index is being added or reviewed, or an index must be built without blocking writes.
---

# PostgreSQL indexing recipes

| Facing | Read |
|---|---|
| Deleting a parent row is slow, or "which foreign keys lack an index?" | [foreign key indexes](references/foreign-key-indexes.md) |
| A hot query reads a few columns besides its filter | [covering index](references/covering-index.md) |
| Queries filter on `lower(col)`, a jsonb key or another computed value | [expression index](references/expression-index.md) |
| A huge append-only table is queried by time range | [BRIN index](references/brin-index.md) |
| An index must be built on a live table without blocking writes | [CREATE INDEX CONCURRENTLY](references/create-index-concurrently.md) |

- Every index slows every insert and every update of its columns, and can
  block HOT updates. Add one for a measured query, not in case.
- Before adding, look for unused ones: `pg_stat_user_indexes.idx_scan = 0`
  over a representative period, checked on every node that serves reads.
- Judge a plan with `EXPLAIN (ANALYZE, BUFFERS)` on production-sized data;
  on a small table the planner rightly prefers a sequential scan.
