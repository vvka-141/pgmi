---
name: pgmi-design
description: Designs a pgmi project and its deploy.sql. Covers which decisions belong in deploy.sql rather than in a CLI flag, choosing transaction boundaries and phases, making every redeploy converge onto a database that already holds data, building a preview or dry run in SQL through a parameter, and modelling the application as data, constraints and operations inside PostgreSQL. Use when planning a feature, a schema change or a data migration, when choosing deploy.sql's transactions or phases, when a change must be safe to redeploy, or when looking for a --dry-run, --skip, --only or --rollback flag that pgmi does not have.
---

# Designing a pgmi project

pgmi connects, loads every project file and the `--param` values into the
session, and runs `deploy.sql`. Everything else is a decision this project's
SQL makes. Before designing a change, read `deploy.sql` and the README: they
already encode this project's answers, and those answers win over the
defaults below.

The session API (views, columns, the test macro) is in `pgmi ai contract`;
the execution contract is in `pgmi ai` and the exit codes in `pgmi help deploy`. Look them up there.

## Where each decision lives

| Decision | Lives in |
|---|---|
| What runs, in what order | `deploy.sql`: a query over the session views |
| Transaction boundaries, phases | `deploy.sql`: `BEGIN` / `COMMIT` at top level |
| Re-run every deploy or apply once | `deploy.sql` (and the project's file conventions) |
| Environment differences | `deploy.sql` branching on a parameter |
| Preview, skip a phase, run a subset | `deploy.sql` branching on a parameter |
| Lock and statement limits | `SET LOCAL lock_timeout`, `statement_timeout` in SQL |
| Retry | Rerun the deploy; it is safe because every step converges |
| Rollback of a bad release | A new, forward change deployed the same way |
| Connection, credentials, parameter values, verbosity | CLI flags, `pgmi.yaml`, `--params-file`, `PGMI_PARAM_*` |

The CLI has no `--dry-run`, `--skip-*`, `--only-*`, `--rollback` or
transaction flags, and none will appear. Do not search for one. Pass a
parameter (`--param preview=true`) and let `deploy.sql` act on it.

Each `--param key=value` becomes the session setting `pgmi.<key>` (key
lowercased), readable as `current_setting('pgmi.<key>', true)`, which is NULL
when the parameter was not given. A project may declare and validate its
parameters on top of that; check its `deploy.sql` and README before adding
one. Secrets go in a `PGMI_PARAM_<NAME>` environment variable or `--params-file`,
never on the command line.

## Transactions: the default

How pgmi runs `deploy.sql`, in two sentences: everything up to the first
top-level `COMMIT` runs as one transaction; each top-level statement after it
autocommits, as in psql. Design from that:

- Put the work in one transaction that ends with the test gate, then
  `COMMIT`. A failing file or test then applies nothing.
- Work that must be gated by the tests goes before that `COMMIT`.
- Only statements PostgreSQL refuses inside a transaction or a function
  (`CREATE INDEX CONCURRENTLY`, `VACUUM`) go after it, at top level, one
  statement each, written to be re-runnable.
- `COMMIT` inside a `DO` block or a procedure fails whenever a transaction
  block surrounds it, so a file loop commits once, at the top-level `COMMIT`,
  never per file.

Phases, lock limits, the concurrent-index recipe and the preview gate:
[references/transactions.md](references/transactions.md).

## Redeploys onto existing data

The deploy that matters runs against a database full of data, not an empty
one. Design every change for that database:

- Know which files re-run every deploy and which run once. That is this
  project's choice, made in `deploy.sql`; read it before adding a file.
- A file that re-runs must converge: the second run changes nothing and
  fails nothing. A file that runs once must be correct against the rows
  that exist when it runs.
- A changed table needs an explicit change (`ALTER TABLE ...`) and a test
  that asserts the new shape; `IF NOT EXISTS` does not do it (pgmi-layers,
  references/physical.md).
- Reference data converges from a file, so the file stays the source of
  truth (references/redeploy.md). Seed data goes through the writer functions,
  so their rules hold (pgmi-layers).

Patterns and runnable proofs: [references/redeploy.md](references/redeploy.md).

## Preview and dry run

A preview runs the whole deploy, prints what it would do, then refuses to
commit. Add one gate as the last statement before the top-level `COMMIT`:

```sql
-- deploy.sql, after the test gate, before COMMIT
DO $$
BEGIN
    IF COALESCE(current_setting('pgmi.preview', true), 'false') = 'true' THEN
        RAISE EXCEPTION 'preview: rolled back, nothing applied';
    END IF;
END $$;

COMMIT;
```

`pgmi deploy . -d <db> --param preview=true` then runs every file and test
and rolls all of it back. The deploy reports a failure, which is the honest
result: nothing landed. Statements after the `COMMIT` never run in a
preview, so the preview does not cover them. Never combine a preview with
`--overwrite`: it drops the database before `deploy.sql` starts.

## The application is the dataset

Model the application inside PostgreSQL: tables with constraints that make
invalid states unrepresentable, operations as functions named by what they
do, and a test for every operation. Whatever calls the database (a service,
a job, an agent) calls those operations rather than writing tables directly.
Design in that order: data, operations, tests, then how callers reach them.

Details: [references/data-model.md](references/data-model.md).

## Before you hand back a design

- Every decision in the table above sits where the table says.
- The change redeploys onto the current database: the deploy is green on a
  fresh database and again on the one it just deployed.
- Anything after the top-level `COMMIT` is re-runnable on its own.
- No new parameter duplicates one the project already declares.
