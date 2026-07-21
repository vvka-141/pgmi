---
title: "From Seed Scripts to Desired-State Reference Data in PostgreSQL"
date: 2026-07-21
author: "Alexey Evlampiev"
description: "Why row-oriented seed scripts become brittle when they mix desired data with construction procedure, and how PostgreSQL can validate, diff, load, and reconcile a natural-key catalog inside one transaction."
weight: 5
---

# From Seed Scripts to Desired-State Reference Data in PostgreSQL

*Version the catalog that should exist — not the procedure that inserts it.*

*By Alexey Evlampiev*

A seed script records *how* to construct a catalog. What you need to version is
the catalog that *should exist*.

The difference barely matters when `seed.sql` is twelve lookup rows. It starts to
matter once roles reference permissions, workflow states reference their legal
transitions, and plan tiers reference feature flags — once every edit has to
preserve foreign-key order by hand. At that point the script has quietly become a
hand-maintained topological sort of your data model: an acyclic insert-dependency
graph, revised on every new relationship and enforced by nothing but SQLSTATE
23503 at deploy time.

This article is about that kind of data — the catalogs a schema cannot function
without. Application roles and their permission grants. Status catalogs and
which transitions are legal. Tax rate tables and ISO currency codes. Call it
**desired-state reference data**: what you want to version is the catalog that
*should exist*, not the procedure that builds it. (It is not immutable — tax
rates and permissions change constantly — which is exactly why the maintenance
problem below bites.) Teams routinely carry tens of such tables; the worked
example here is three, and it already exhibits the core failure modes.

## One file doing two jobs

Here is the seed script for a three-table role/permission catalog, the way these
scripts actually look in the wild:

```sql
INSERT INTO permission (permission_id, key, description) VALUES
    (1, 'invoice.read',    'View invoices'),
    (2, 'invoice.approve', 'Approve invoices for payment'),
    (3, 'report.run',      'Run financial reports'),
    (4, 'catalog.edit',    'Maintain product catalog entries');

INSERT INTO role (role_id, key, description) VALUES
    (1, 'auditor',         'Read-only access for audits'),
    (2, 'controller',      'Approves spending'),
    (3, 'catalog_manager', 'Owns the product catalog');

-- must run last, and every pair is a number puzzle
INSERT INTO role_permission (role_id, permission_id) VALUES
    (1, 1), (1, 3),
    (2, 1), (2, 2), (2, 3),
    (3, 4);
```

**The ordering ceremony.** `role_permission` must come last; swap the statements
and the deploy fails with `violates foreign key constraint` (SQLSTATE 23503).
With three tables that is easy to keep straight. Each new relationship adds an
edge to the dependency graph you are sorting by hand. (PostgreSQL does offer an
escape hatch — constraints declared `DEFERRABLE INITIALLY DEFERRED` are checked
at commit rather than per statement — but deferring the check means violations
surface only at COMMIT, far from the statement that caused them, and it does
nothing for the surrogate keys or the validation gaps below.)

**The surrogate-key puzzle.** What does `(2, 3)` mean? You have to cross-
reference two other statements to find out it grants `report.run` to
`controller`. The diff of a permission change is a diff of integer pairs.
Django's fixture system has surfaced this exact failure mode repeatedly —
hardcoded primary keys that collide and re-insert instead of updating
([ticket #31531](https://code.djangoproject.com/ticket/31531), and downstream
reports like [NetBox #10940](https://github.com/netbox-community/netbox/issues/10940),
where re-running `loaddata` hits duplicate keys).

**Non-idempotent re-runs.** Run the script twice and the second run dies on a
unique violation — unless every statement grows its own `ON CONFLICT` clause,
at which point the script is no longer data anyone can read.

**The schema-evolution tax.** Add a `NOT NULL` column without a default to
`permission` and every seed INSERT that omits it breaks. The script has to be
revised in lockstep with migrations. (This tax never fully disappears — it
moves, as we'll see, from a hundred INSERTs into one loader contract.)

**Constraints validate structure, not intent.** A foreign key catches a
genuinely nonexistent id: `(2, 99)` where permission 99 does not exist fails
immediately with SQLSTATE 23503. What a foreign key *cannot* catch is an opaque
`(2, 3)` that satisfies the constraint while pointing at the wrong permission —
the mapping is valid SQL and wrong data. Nothing checks that the catalog is
internally coherent.

None of these is a fringe observation. Microsoft's Entity Framework team renamed
EF Core's "data seeding" feature to "model managed data" because, in their own
words, the old name *"sets incorrect expectations, as the feature has a number
of limitations and is only appropriate for specific types of data"* — the
documented limits: hardcoded primary keys even when the database would generate
them; data removal when a key changes; and explicit foreign-key values for
every child row ([EF Core docs](https://learn.microsoft.com/en-us/ef/core/modeling/data-seeding);
this describes the `HasData` model-managed path, not EF's runtime `UseSeeding`
hook). When a first-party vendor renames the category to lower expectations,
the category has a problem.

The common thread is that one file is doing two jobs: it encodes both the
**desired catalog** and the **procedure to construct it** — statement order,
surrogate keys, conflict handling. Separate those, and most of the list above
stops being your problem.

## Reference data wants to be data

Here is the same catalog as one JSON document, `seeds/roles.json`:

```json
{
  "permissions": [
    {"key": "invoice.read",    "description": "View invoices"},
    {"key": "invoice.approve", "description": "Approve invoices for payment"},
    {"key": "report.run",      "description": "Run financial reports"},
    {"key": "catalog.edit",    "description": "Maintain product catalog entries"}
  ],
  "roles": [
    {"key": "auditor",    "description": "Read-only access for audits",
     "grants": ["invoice.read", "report.run"]},
    {"key": "controller", "description": "Approves spending",
     "grants": ["invoice.read", "invoice.approve", "report.run"]},
    {"key": "catalog_manager", "description": "Owns the product catalog",
     "grants": ["catalog.edit"]}
  ]
}
```

Three properties changed at once. Relationships are expressed by **natural keys**
(`"grants": ["invoice.read", ...]`), so no surrogate id appears anywhere in the
document — the database stays free to generate them. There is **no statement
order**, so there is nothing to topologically sort by hand. And a reviewer can
read a permission change in the diff without decoding integer pairs.

The target schema is unremarkable — three tables, a unique natural key on each
parent, a composite primary key on the join, and a `deprecated_at` column we'll
use for reconciliation:

```sql
CREATE TABLE role (
    role_id       int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,
    description   text NOT NULL,
    deprecated_at timestamptz
);
CREATE TABLE permission (
    permission_id int GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    key           text NOT NULL UNIQUE,
    description   text NOT NULL,
    deprecated_at timestamptz
);
CREATE TABLE role_permission (
    role_id       int NOT NULL REFERENCES role,
    permission_id int NOT NULL REFERENCES permission,
    PRIMARY KEY (role_id, permission_id)
);
```

What turns the document back into that graph is a PostgreSQL feature pair that
has been stable for over a decade: `jsonb_to_recordset` (9.4+) turns a JSON array
into typed rows, and
[data-modifying CTEs](https://www.postgresql.org/docs/current/queries-with.html)
(9.1+) chain INSERTs through `RETURNING` so a later insert can use the keys the
earlier one generated — in one statement:

```sql
WITH doc AS (
    SELECT seed_content::jsonb AS j          -- however the file reaches the session
),
new_permission AS (
    INSERT INTO permission (key, description)
    SELECT p.key, p.description
    FROM doc,
         jsonb_to_recordset(doc.j -> 'permissions')
             AS p(key text, description text)
    ON CONFLICT (key) DO UPDATE
        SET description = EXCLUDED.description,
            deprecated_at = NULL
    RETURNING permission_id, key
),
new_role AS (
    INSERT INTO role (key, description)
    SELECT r.key, r.description
    FROM doc,
         jsonb_to_recordset(doc.j -> 'roles')
             AS r(key text, description text)
    ON CONFLICT (key) DO UPDATE
        SET description = EXCLUDED.description,
            deprecated_at = NULL
    RETURNING role_id, key
)
INSERT INTO role_permission (role_id, permission_id)
SELECT nr.role_id, np.permission_id
FROM doc,
     jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
     new_role nr,
     new_permission np
WHERE nr.key = r.key
  AND np.key = ANY (r.grants)
ON CONFLICT DO NOTHING;
```

One statement upserts both parent tables and inserts every grant the file
declares — and the JSON never lands in a column; `jsonb_to_recordset` parses it
into ordinary normalized rows on the way through. The foreign keys are resolved
by joining the `RETURNING` sets on natural keys, and `jsonb_to_recordset` maps
the JSON `grants` array straight onto a `text[]` column. Run it against an empty
catalog: 3 roles, 4 permissions, 6 grants. Run it again and the generated ids do
not churn — `ON CONFLICT` updates the existing rows in place — descriptions are
refreshed, and no duplicate grants appear. (Id stability is verified against the
reproducible demo; see the note under Sources.)

Two mechanics here deserve precision, because they are where copy-adapted
versions go wrong:

**The `RETURNING` chain is load-bearing, not stylistic.** All sub-statements of
a `WITH` execute against the same snapshot — the PostgreSQL documentation is
explicit that they cannot "see" one another's effects on the target tables. A
final INSERT that tried `SELECT role_id FROM role WHERE key = ...` would not
find rows inserted two CTEs earlier *in the same statement*. `RETURNING` is the
sanctioned channel between them, which is exactly why this pattern composes: the
data flows through the query, never through re-reads.

**`DO UPDATE`, not `DO NOTHING`, on the parent tables.** `ON CONFLICT DO NOTHING`
returns no row for a conflict, so on a re-run every already-existing role and
permission silently drops out of the `RETURNING` chain — and its grants vanish
from the final join. I verified this trap directly: against a catalog where one
role and one permission pre-existed, the `DO NOTHING` variant loaded 3 of 6
grants without any error. `DO UPDATE` returns the row whether it was inserted or
updated, keeps the chain complete, and applies the declared descriptions in the
same pass. (On the leaf table, where nothing chains further, `DO NOTHING` is
exactly right.)

A note on formats, since reference data arrives in more than one: the same
pattern works for XML via
[`xmltable()`](https://www.postgresql.org/docs/current/functions-xml.html) —
worth knowing when the source of truth is an MDM or ERP export — and CSV handles
flat lists well but has no native representation for the nested `grants` array
that makes this document a graph.

## Converging to the desired state

The statement above only *adds*. It upserts nodes and inserts declared grants —
but if you delete `report.run` from `auditor`'s grants and redeploy, the old
grant is still in the database. The file no longer describes the catalog, and no
`ON CONFLICT` clause will ever notice, because convergence is not something a
single INSERT can express. "Load the whole graph in one statement" is a good
hook and a false promise; the honest unit is **one transaction**, and inside it
the work is a short pipeline.

Because the document and the live tables are queryable side by side, the first
step can be a **pre-apply diff** — computed *before* anything mutates:

```sql
WITH doc AS (SELECT seed_content::jsonb AS j),
seed_role AS (
    SELECT r.key, r.description
    FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text, description text)
),
seed_edge AS (
    SELECT r.key AS role_key, g.grant_key AS perm_key
    FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
         unnest(r.grants) AS g(grant_key)
)
-- attribute changes: what the upsert is about to overwrite
SELECT format('role %s: live=%L seed=%L', r.key, r.description, s.description)
FROM role r JOIN seed_role s USING (key)
WHERE r.description IS DISTINCT FROM s.description
UNION ALL
-- grants the file no longer declares: what convergence is about to delete
SELECT format('grant - %s -> %s', ro.key, pe.key)
FROM role_permission rp
JOIN role ro ON ro.role_id = rp.role_id
JOIN permission pe ON pe.permission_id = rp.permission_id
WHERE ro.key IN (SELECT key FROM seed_role)
  AND NOT EXISTS (SELECT 1 FROM seed_edge se
                  WHERE se.role_key = ro.key AND se.perm_key = pe.key);
```

The same shape reports added grants and departed nodes. Running it first is what
makes it honest: an upsert that overwrites a live description would otherwise
*erase the evidence* that someone hand-edited the catalog in production. The
database cannot tell a hand-edit from a legitimate file change — both are
`live IS DISTINCT FROM seed` — so the report says what it can actually know:
*here is what this deploy is about to change.* Run it after the upsert and it is
always empty.

Then convergence, where nodes and edges want different policies:

**Edges converge by deletion.** A `role_permission` row is a pure relationship —
no identity, no history, nothing references it. So the join table should match
the file exactly: insert what is declared (done above), delete what is not.

```sql
WITH doc AS (SELECT seed_content::jsonb AS j),
seed_role AS (SELECT r.key FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text)),
desired AS (
    SELECT ro.role_id, pe.permission_id
    FROM doc,
         jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
         unnest(r.grants) AS g(grant_key),
         role ro, permission pe
    WHERE ro.key = r.key AND pe.key = g.grant_key
)
DELETE FROM role_permission rp
USING role ro
WHERE rp.role_id = ro.role_id
  AND ro.key IN (SELECT key FROM seed_role)              -- only roles the file owns
  AND NOT EXISTS (SELECT 1 FROM desired d
                  WHERE d.role_id = rp.role_id AND d.permission_id = rp.permission_id);
```

**Nodes deprecate, they don't delete.** A role or permission has identity, an
audit trail, and referential dependents — transactional data points at it. A
hard `DELETE` either fails on those foreign keys or, with cascades, takes history
with it. The defensible default is soft deprecation:

```sql
WITH doc AS (SELECT seed_content::jsonb AS j),
seeded AS (SELECT r.key FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text))
UPDATE role SET deprecated_at = now()
WHERE deprecated_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM seeded s WHERE s.key = role.key);
```

A deprecated role can be excluded from new assignments — application queries
filter on `deprecated_at` — while keeping its identity, its audit trail, and
referential integrity. Its existing grants are left **frozen**: once the role
leaves the file, it is no longer the file's to converge. (The counterpart is the
`deprecated_at = NULL` in the upsert above, so a key that returns to the file is
resurrected.) The same deprecation applies to permissions. One caveat belongs
here: this all assumes the seed file *owns* the table. If the application can
also insert catalog rows at runtime, add a provenance column — `managed_by`,
distinguishing seed-owned from runtime rows — and scope every reconciliation
step to seed-owned rows.

## Seed data gets a test suite

Once the seed file is data in the session, validating it *before any insert* is
a query, not a wish:

```sql
-- every grant must reference a declared permission
WITH doc AS (SELECT seed_content::jsonb AS j),
granted AS (
    SELECT DISTINCT g.grant_key
    FROM doc,
         jsonb_to_recordset(doc.j -> 'roles') AS r(key text, grants text[]),
         unnest(r.grants) AS g(grant_key)
),
declared AS (
    SELECT p.key
    FROM doc, jsonb_to_recordset(doc.j -> 'permissions') AS p(key text)
)
SELECT string_agg(grant_key, ', ')
FROM granted g
WHERE NOT EXISTS (SELECT 1 FROM declared d WHERE d.key = g.grant_key);
```

This is the check a foreign key can't do for you: a grant naming a permission
the file never declares. In the natural-key document such a grant simply fails
to resolve in the loader's join — no row is built, no constraint fires, the role
is quietly missing a permission. So the file validates itself first: non-NULL
result, raise an exception, abort the deploy, nothing touched. (Note `NOT EXISTS`
rather than `NOT IN` — a single permission entry with a missing `key` yields a
NULL that silently disarms a `NOT IN` gate; three-valued logic has no place in a
safety check.) The same shape checks duplicate natural keys and missing required
fields — relational integrity across records, which ordinary structural schemas
generally do not express cleanly. (JSON Schema still earns its place
in CI for structural linting — types, required fields, key patterns; the two
validations compose.)

A second gate runs *after* the load: invariants of the seeded catalog itself.
"Every active role has at least one permission." "Status transitions form a
DAG." These are ordinary SQL tests, and if one fails, the deployment — schema
changes and seed data together — should not commit.

## What the ecosystem does today

Fairness requires naming the closest neighbor first. **Liquibase** has the most
capable data facility of the migration tools I surveyed:
[`loadUpdateData`](https://docs.liquibase.com/change-types/load-update-data.html)
loads a CSV with a declared `primaryKey` (composite keys allowed) and performs
insert-or-update against it — genuine idempotent, natural-key seeding, credit
where due. Liquibase can also gate a change on database state:
[SQL preconditions](https://docs.liquibase.com/community/user-guide-5-0/what-are-preconditions)
run a query and check the result. What it does not do is make the *seed file
itself* a relation your deployment logic can query — so validating the file,
diffing it against the live catalog, and reconciling removed rows aren't
expressible against the CSV before it loads. The format is flat (the `grants`
graph needs a separate join-table file with its own key discipline), and
environment targeting is by context expression rather than arbitrary deploy
logic.

**Flyway** approaches data through `${placeholder}` substitution, repeatable
`R__` migrations — its documentation lists "bulk reference data reinserts" as a
use case, with idempotency explicitly on the author ("It is your responsibility
to ensure the same repeatable migration can be applied multiple times") — and
callbacks such as `afterMigrate`. A structural point worth knowing: a seed run
in an `afterMigrate` callback is a separate step from the migrations, so schema
and seed do not share one atomic transaction; a failure there leaves the schema
applied. **Sqitch** has environment handling via named targets and `--set`
variables but no first-class data-file facility. **dbt seed** loads CSVs and
draws its own boundary — the docs recommend against it for large files. These are
all reasonable tools, and several take non-SQL changelogs (Liquibase in
XML/YAML/JSON) or non-SQL script migrations. But their documented reference-data
paths treat the data as executable migration logic or as flat files loaded into
tables; none of the facilities surveyed here stages an arbitrary non-SQL project
file as a *queryable relation* inside the deployment session — which is the one
mechanism every technique in this article depends on.

## Running it: the session as the delivery mechanism

Everything above is plain PostgreSQL — the open question is how `seeds/roles.json`
reaches the session. I maintain [pgmi](https://github.com/vvka-141/pgmi), a small
deployment tool built around exactly that gap: it loads every supported UTF-8
non-test project file — SQL or otherwise — into session-scoped views, then
executes the `deploy.sql`
you write (deploy.sql is the one file it runs rather than exposes). The seed file
arrives as a row:

```sql
SELECT content::jsonb
FROM pg_temp.pgmi_source_view
WHERE path = './seeds/roles.json';
```

So the abstract `WITH doc AS (SELECT seed_content::jsonb ...)` becomes that query,
verbatim. A minimal project:

```
seed-demo/
├── deploy.sql
├── migrations/001_catalog.sql
├── seeds/roles.json
└── __test__/test_catalog_invariants.sql
```

`deploy.sql` opens one transaction and runs the pipeline in order: validate the
file, apply migrations, print the pre-apply diff, load and converge the catalog,
deprecate what left the file, run the test suite, commit.

```sql
BEGIN;
DO $$ ... $$;                 -- validate the seed file
DO $$ ... EXECUTE ... $$;     -- apply migrations in path order
DO $$ ... $$;                 -- pre-apply diff (before any mutation)
WITH doc AS (...) ...;        -- upsert nodes + insert declared grants
WITH doc AS (...) DELETE ...; -- converge edges
WITH doc AS (...) UPDATE ...; -- deprecate departed nodes

SAVEPOINT _tests;
CALL pgmi_test();             -- expands to the __test__/ suite, savepoint-isolated
ROLLBACK TO SAVEPOINT _tests;
COMMIT;
```

![Seed data flow: repository files become queryable session views; deploy.sql validates, diffs, loads, converges, deprecates, and tests inside one transaction — tests pass means COMMIT, any failure rolls everything back](/pgmi/docs/diagrams/a02-seed-data-flow.drawio.svg)

That single `BEGIN ... COMMIT` is what earns the emphasis: **the seed data
commits in the same transaction as the schema migration, gated by the same
tests.** There is no "migrations succeeded, seeds failed halfway" state to clean
up. The complete runnable project is in
[`examples/seed-demo/`](https://github.com/vvka-141/pgmi/tree/main/examples/seed-demo);
what follows is an *abridged, annotated* transcript of it — real `pgmi` output,
with elisions marked `...` and my notes after `--` or in parentheses:

```
$ pgmi deploy ./seed-demo --connection $DB --force
seed file validated: ./seeds/roles.json
applying ./migrations/001_catalog.sql
plan: grant + auditor -> invoice.read
plan: grant + auditor -> report.run
... (6 grants added on first load)
[pgmi] Test suite completed (2 steps)
✓ 6 files loaded, 1 test macro(s) expanded in 1.95s          (exit 0)

$ pgmi deploy ./seed-demo --connection $DB --force    # again, nothing changed
✓ 6 files loaded, 1 test macro(s) expanded in 1.93s          (exit 0)

# drop report.run from auditor's grants in roles.json, redeploy:
$ pgmi deploy ./seed-demo --connection $DB --force
plan: grant - auditor -> report.run
✓ 6 files loaded, 1 test macro(s) expanded in 1.62s          (exit 0)

$ psql $DB -c "SELECT r.key AS role, count(rp.*) AS grants
               FROM role r LEFT JOIN role_permission rp USING (role_id)
               WHERE r.deprecated_at IS NULL GROUP BY r.key ORDER BY r.key;"
      role       | grants
-----------------+--------
 auditor         |      1     -- report.run converged away, not left behind
 catalog_manager |      1
 controller      |      3
```

And the failure that matters: a structurally valid seed that violates a catalog
invariant (a role with zero permissions) passes validation, loads — and then the
test fails and takes the *entire* deployment with it, schema included:

```
$ pgmi deploy ./seed-demo --connection $DB --force
seed file validated: ./seeds/roles.json
applying ./migrations/001_catalog.sql
[pgmi] Test: ./__test__/test_catalog_invariants.sql
✗ Failed after 1.54s
pgmi: error: execution failed: ERROR: catalog invariant violated:
  1 active role(s) with no permissions (SQLSTATE P0001)      (exit 13)

$ psql $DB -c "\dt"
Did not find any relations.
```

(A seed granting an undeclared permission fails the same way, earlier, at the
validation gate — exit 13, nothing touched.)

The same session also carries deploy-time parameters read from layerable
`.env`-format files (`--params-file`, surfaced as `current_setting('pgmi.key',
true)`), so environment-conditional seeding — "load `./seeds/dev/` only in
development" — is one `IF` in deploy.sql, and a CI job can *generate* a seed file
and drop it beside the static ones. Seeding *identities and secrets* — an initial
admin, a service principal — is a genuinely different problem, with its own sharp
edges (cleartext credentials in DDL, server-log exposure under `log_statement`).
It deserves its own treatment, not a paragraph here.

## Honest limits

**Small catalogs don't need this.** If your reference data is one flat table of a
dozen rows, a plain INSERT with `ON CONFLICT` is clearer than any of the above.
The techniques pay off when the data is a graph and the team edits it often.

**Idempotent upserts are not free.** `ON CONFLICT DO UPDATE` rewrites every parent
on every run — new row versions, WAL, fired triggers, bumped audit timestamps —
even when nothing changed. Existing row identities stay stable, but a conflicting
insert still consumes a sequence value before the conflict is detected, so the
backing identity sequences advance and
[gaps are expected](https://www.postgresql.org/docs/current/functions-sequence.html).
The obvious guard, `WHERE description IS DISTINCT FROM
EXCLUDED.description`, has a catch: a row it *skips* is a row `ON CONFLICT` does
not `RETURN`, which breaks the `RETURNING` chain the same way `DO NOTHING` does.
If the write cost bites, split the parent upsert out of the chain rather than
making it conditional.

**Key renames need a decision.** To this system, renaming a natural key is a
delete-plus-insert: the old key deprecates, the new key is created fresh, and any
history keyed to the old identity stays with it. If continuity matters, rename in
the database and the file together, not in the file alone.

**This is not a bulk-loading path.** File content travels as text through a
session; for genuinely large datasets, PostgreSQL's own guidance is
[`COPY`](https://www.postgresql.org/docs/current/populate.html). Reference
catalogs are hundreds to low thousands of rows; if you are seeding millions, you
are doing ingestion, and ingestion wants different tools. Data that arrives
continuously — from a lakehouse, an ETL job, CDC — needs its own delivery and
reconciliation semantics and does not belong in a deploy.

**The transactional guarantee has preconditions.** Schema-plus-seed atomicity
holds because everything runs between one `BEGIN` and one `COMMIT` on one session
— which means a direct connection or a session-mode pooler; transaction-mode
poolers recycle backends and break session-scoped state. And DDL that cannot run
in a transaction block (`CREATE INDEX CONCURRENTLY`, notably) cannot live inside
this envelope.

## Takeaway

Row-oriented seed scripts become brittle for a structural reason: a script
encodes *how* to insert — order, surrogate keys, conflict handling — when what
you want to version is *what should exist*. Move the catalog into a document keyed by
natural identifiers, and PostgreSQL supplies the rest: `jsonb_to_recordset` to
type it, data-modifying CTEs to load the declared graph in one statement, a
pre-apply diff to show what a deploy will change before it changes it, plain
`DELETE`/`UPDATE` to converge edges and deprecate departed nodes, and a test gate
that refuses to commit a catalog your own invariants reject — all in one
transaction. Delivery into the session is the remaining integration choice in
this pattern; the semantics are yours either way.

Seed data as data. Convergence over insertion. Commit only what your invariants
accept.

---

## Sources

- Microsoft, [EF Core data seeding](https://learn.microsoft.com/en-us/ef/core/modeling/data-seeding) — the "model managed data" rename and its documented limitations (`HasData` path)
- Liquibase, [`loadUpdateData`](https://docs.liquibase.com/change-types/load-update-data.html) — idempotent CSV loading against a declared primary key; [preconditions](https://docs.liquibase.com/community/user-guide-5-0/what-are-preconditions) — SQL checks against database state
- Redgate, [Flyway documentation](https://documentation.red-gate.com/flyway) and [repeatable migrations](https://documentation.red-gate.com/flyway/flyway-concepts/migrations/repeatable-migrations) — placeholders, `R__` for bulk reference-data reinserts ("It is your responsibility to ensure the same repeatable migration can be applied multiple times"), callbacks
- Sqitch, [sqitch-deploy manual](https://sqitch.org/docs/manual/sqitch-deploy/) — targets and `--set` variables; no first-class data-file facility
- dbt, [seeds documentation](https://docs.getdbt.com/docs/build/seeds) — CSV loading and its own "not for large files" boundary
- PostgreSQL documentation: [WITH queries §7.8 (data-modifying statements)](https://www.postgresql.org/docs/current/queries-with.html); [JSON functions (`jsonb_to_recordset`)](https://www.postgresql.org/docs/current/functions-json.html); [INSERT ... ON CONFLICT](https://www.postgresql.org/docs/current/sql-insert.html); [`xmltable`](https://www.postgresql.org/docs/current/functions-xml.html); [Populating a database](https://www.postgresql.org/docs/current/populate.html)
- Django ticket [#31531](https://code.djangoproject.com/ticket/31531); [NetBox #10940](https://github.com/netbox-community/netbox/issues/10940) — hardcoded-PK fixture re-run failures in the wild
- [JSON Schema specification](https://json-schema.org/specification) — structural linting of seed files in CI
- All SQL verified on PostgreSQL 17.10 against the runnable demo in [`examples/seed-demo/`](https://github.com/vvka-141/pgmi/tree/main/examples/seed-demo): initial load, idempotent re-run (generated ids stable), grant removal (converged by delete), description drift (reported before overwrite), role removal (deprecated, grants frozen), the validation gate, and the invariant-failure rollback (schema and seed removed together).
