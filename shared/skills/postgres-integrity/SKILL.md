---
name: postgres-integrity
description: PostgreSQL recipes that make the database itself enforce business rules instead of application code or validation triggers. Covers partial unique indexes (one active row per key), exclusion constraints with btree_gist (no overlapping bookings or date ranges per key), deferrable foreign keys and unique constraints (circular references, swapping positions in one transaction), CHECK, NOT NULL and composite foreign keys in place of validation triggers, and domains for reused value rules such as an email or a positive amount. Use when a rule must hold under concurrent writes, when tempted to write a trigger that raises on bad data, when an insert order or a swap trips a constraint, or when the same CHECK is copied across tables.
---

# PostgreSQL integrity recipes

| Facing | Read |
|---|---|
| "At most one row per X may be Y" (one active, one primary) | [partial unique index](references/partial-unique-index.md) |
| "No two rows per X may overlap in time or range" | [exclusion constraint](references/exclusion-constraint.md) |
| Rows that reference each other, or a swap that trips a unique key | [deferrable constraints](references/deferrable-constraints.md) |
| A trigger that only validates and raises | [CHECK over trigger](references/check-over-trigger.md) |
| The same value rule on columns in several tables | [domains](references/domains.md) |

- A rule checked by a `SELECT` before the write races under concurrent
  writers; a declared constraint does not. Reach for a constraint first.
- Name every constraint, and in its test catch the one SQLSTATE it raises and
  check `CONSTRAINT_NAME`.
- A constraint added to a populated table fails the deploy on existing
  violations. Clean the data in the same change, before the constraint.
