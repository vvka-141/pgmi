---
name: postgres-analytics
description: PostgreSQL recipes for reports, counts and aggregates computed inside the database. Covers conditional aggregates with FILTER, subtotals and grand totals with GROUPING SETS, ROLLUP, CUBE and GROUPING(), window functions for running totals, rank within a group, previous row with lag and latest row per group with DISTINCT ON, and approximate distinct counts with the hll extension. Use when writing a report, a dashboard query, a count or sum per group, a subtotal, a running total, a top-N or latest-per-group query, or a unique-visitor count, or when replacing SUM(CASE ...), UNION ALL of GROUP BY queries, self-joins or correlated subqueries.
---

# PostgreSQL analytics recipes

| Facing | Read |
|---|---|
| Several conditional counts or sums per group, `SUM(CASE ...)` | [FILTER clause](references/filter-clause.md) |
| Detail rows plus subtotals and a grand total | [grouping sets](references/grouping-sets.md) |
| Running total, rank in a group, previous row, latest row per group | [window functions](references/window-functions.md) |
| Distinct counts over large data or across rollup periods | [approximate distinct](references/approximate-distinct.md) |

Compute the report in one query over the base rows. Several queries stitched
together in application code, or one query per figure, rescan the data and
drift apart when one of them changes. Pin a report's result in a test with a
hand-computed expected set, compared with `EXCEPT` in both directions.
