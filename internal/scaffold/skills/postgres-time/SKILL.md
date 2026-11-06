---
name: postgres-time
description: PostgreSQL recipes for storing times, dates and validity periods. Covers timestamptz versus timestamp and date, time zones and AT TIME ZONE, what now() returns inside a deploy or test versus clock_timestamp(), and tstzrange or daterange columns with containment, overlap and half-open bounds. Use when adding a timestamp or date column, converting between time zones, grouping by day, or modelling a price, contract or assignment that is valid from one time to another.
---

# PostgreSQL time recipes

| Facing | Read |
|---|---|
| A column records when something happened, or a time-zone bug | [timestamptz](references/timestamptz.md) |
| A row is valid from one time to another | [range types](references/range-types.md) |

Default to `timestamptz` for instants, `date` for calendar days, and one range
column for a period. Periods for one key that must not overlap need an
exclusion constraint: see the postgres-integrity skill.
