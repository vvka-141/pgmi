# Desired-state reference-data seeding

The runnable companion to the article
[*From Seed Scripts to Desired-State Reference Data in PostgreSQL*](https://vvka-141.github.io/pgmi/articles/seed-scripts-are-topological-sorts/).

A role/permission catalog is versioned as one JSON document
(`seeds/roles.json`). `deploy.sql` turns that document back into a relational
graph and **converges the database to it** — in one transaction, gated by the
catalog's own invariants.

![Seed data flow: repository files become queryable session views; deploy.sql validates, diffs, loads, converges, deprecates, and tests inside one transaction](../../docs/diagrams/a02-seed-data-flow.drawio.svg)

`deploy.sql` runs seven steps in order, all in one `BEGIN ... COMMIT`:

1. **Validate** the seed file — undeclared grants or duplicate keys `RAISE`.
2. **Apply migrations** in path order.
3. **Pre-apply diff** — report what this deploy will change, *before* any mutation.
4. **Load** — upsert nodes and insert declared edges via a `RETURNING`-chained CTE.
5. **Converge edges** — delete grants of seed-owned roles that left the file.
6. **Deprecate nodes** — soft-deprecate roles and permissions that left the file.
7. **Test** — `CALL pgmi_test()` gates the commit on catalog invariants.

## Run it

Any reachable PostgreSQL works; a disposable Docker container is the fastest:

```bash
docker run -d --name pgmi-seed -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:17-alpine
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5434/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5434/postgres"

pgmi deploy . -d pgmi_example --force
```

A clean run loads the catalog (3 roles, 4 permissions, 6 grants) and commits.

## What to try

Each of these is a claim the article makes; each is reproducible here.

- **Idempotent re-run.** Deploy again with no edits: the pre-apply diff is empty,
  generated ids do not change, no duplicate grants appear.
- **Converge a removed edge.** Delete `"report.run"` from `auditor`'s `grants` in
  `seeds/roles.json` and redeploy. The diff prints
  `plan: grant - auditor -> report.run` and the grant is **deleted**, not left
  behind.
- **Report drift before overwrite.** Hand-edit a description in the database
  (`UPDATE permission SET description = '...' WHERE key = 'report.run';`) and
  redeploy. The diff reports the divergence *before* the upsert overwrites it.
- **Deprecate a removed node.** Delete a whole role from the file and redeploy:
  it is soft-deprecated (`deprecated_at` set), not deleted, and its grants are
  left frozen.
- **Rollback on invariant failure.** Add a role with `"grants": []` and redeploy.
  It passes validation, loads, then the invariant test fails — and the whole
  deployment rolls back, schema included (exit 13, no tables created).

Verified on PostgreSQL 17.10.
