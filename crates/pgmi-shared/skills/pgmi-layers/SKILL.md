---
name: pgmi-layers
description: The two layers of a pgmi database — physical (tables, keys, constraints, indexes, policies in run-once files, written in final form and frozen once a kept database ran them) and semantic (views as the only read surface, writer functions as the only writers, *_problem and *_problems refusal functions, one rule in one place, in re-run files). Use when adding or changing a table, a view, a writer function or a business rule, when deciding where a check or a derived fact belongs, when a rule is written twice, when a deploy refuses an edited run-once file, or when a view calls a function once per row.
---

# Physical and semantic layers

| Layer | Holds | Files | Writes | Reads |
|---|---|---|---|---|
| Physical | tables, keys, constraints, indexes, row policies | run-once (`<pgmi-meta idempotent="false">`), plain DDL | nothing else may | — |
| Semantic | views, writer functions, `*_problem` / `*_problems` functions | re-run, `CREATE OR REPLACE` | writer functions, the only writers | views, the read surface |

A rule, a state or a derived fact is stated once, in the semantic layer, and
every reader reads that statement: the writer, the view, the caller, the test.
A rule written in two places is two rules that will disagree.

Where each layer's files live and how `deploy.sql` runs them is this
project's convention: read `deploy.sql`, `AGENTS.md` and the README first. A
template may add a layer on top (the advanced template's API); its own skill
says so.

Neighbouring skills: pgmi-metadata (the `<pgmi-meta>` block), postgres-integrity
and postgres-indexing (constraints and indexes in depth), pgmi-testing (a test
per layer).

## Physical

[Physical layer](references/physical.md) has the details.

- **Final form until data must be kept.** While every database that ran a
  table's file may be recreated (`pgmi deploy . -d <db> --overwrite --force`
  plus the parameters the README lists), edit that file: plain `CREATE TABLE` with every column, constraint and index; no
  `IF NOT EXISTS`, no `ALTER TABLE ... ADD`.
- **Frozen once a kept database ran it.** A run-once file is skipped after it
  ran, so an edit would reach only fresh databases. `deploy.sql` refuses the
  deploy and names the file. The change is a new run-once file sorted after
  it (`ALTER TABLE ...`) with a new id.
- **The database refuses invalid rows**: `NOT NULL`, `CHECK`, keys, a partial
  unique index for "one active per key", an exclusion constraint for
  overlaps. A rule a constraint can state belongs in the constraint.
- **Every foreign key has an index** leading with its columns.
- **No trigger by default.** Constraints, writers and views first; a trigger
  needs a stated reason, and one that keeps a second copy in step is never
  right: derive the copy in a view.

## Semantic

[Semantic layer](references/semantic.md) has writers, refusal functions,
views and "compute once", with a runnable example.

- **Writers** are the only code that changes rows, named
  `<schema>.fn_<verb>_<entity>`. A caller, a job, a seed and a test fixture
  all go through them. A writer returns the affected row (`RETURNS task`),
  never an id or `void`, and answers NULL when its rule refuses: callers test
  `(f(...)).id IS NULL`, never `FOUND` or `INTO STRICT`, which a row of NULLs
  satisfies.
- **A refusal is a function.** `<schema>.fn_<entity>_problem(id, operation)
  RETURNS text` answers a dotted code, `<schema>.<entity>.<reason>`
  (`<schema>.<entity>.not_found` first), or NULL when allowed. For objects
  in `public` the schema is omitted: `fn_task_problem`, `task.not_found`. Field
  validation is `<schema>.fn_<entity>_problems(...) RETURNS TABLE (field
  text, reason text)`, a reason being one word (`required`, `duplicate`).
  The writer writes only `WHERE` the problem is NULL, and a state
  transition also names its from-state in the `UPDATE`
  ([semantic.md](references/semantic.md)); a caller that needs the reason
  asks the same function.
- **Views** are the read surface: one row per what the view is named for,
  plus how the thing is used (counts, latest dates, states), optional facts
  by `LEFT JOIN` or scalar subquery.
- **A state is a column** of the view, computed in one place; a status list
  written as a literal in two places is a missing column. When a caller must
  know in advance, the view carries `<operation>_problem` from the same
  function.
- **Compute once per statement**, not once per row or per column.

## Before calling it done

- [ ] structure in run-once files, complete; views and functions in re-run files
- [ ] every rule the database can enforce is a constraint
- [ ] every write through a writer that returns the row
- [ ] each rule, state and derived fact in one function or column; writer, view and caller read the same one
- [ ] a test per layer: the constraint refuses, the writer refuses and accepts
- [ ] no function called once per row or per column where once per statement would do
