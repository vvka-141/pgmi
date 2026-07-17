---
title: "Your Seed Script Is a Hand-Maintained Topological Sort"
date: 2026-07-17
author: "Alexey Evlampiev"
description: "Why SQL scripts break down as a medium for reference data, and how nested JSON, data-modifying CTEs, and a test gate turn catalog seeding into a graph loaded in one statement, validated and drift-checked by ordinary queries."
weight: 5
---
# Your Seed Script Is a Hand-Maintained Topological Sort

*By Alexey Evlampiev*

Every PostgreSQL project of a certain age has a `seed.sql`. It starts small — a
handful of lookup rows, a few INSERTs — and it works. Then the schema grows the
way real business models grow: roles reference permissions, workflow states
reference allowed transitions, plan tiers reference feature flags, and the seed
script quietly becomes something nobody signed up to maintain. Every child
INSERT needs its parent row to exist first, so the script's statement order is a
topological sort of your data model — maintained by hand, revised on every new
relationship, and enforced by nothing except SQLSTATE 23503 at deploy time.

This article is about that specific kind of data: **invariant reference data** —
the catalogs a schema cannot function without. Application roles and their
permission grants. Status catalogs and which transitions are legal. Tax rate
tables and ISO currency codes. Teams routinely carry tens of such tables — the
worked example below is three tables, and it already exhibits every failure
mode.

## The medium is the problem

The failure modes of script-based seeding are well documented, and they are
worth naming precisely, because each one is a property of the *medium* — SQL
statements — rather than of any particular tool.

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
With three tables that is easy to keep straight. Each new
relationship adds an edge to the dependency graph you are sorting by hand.
(PostgreSQL does offer an escape hatch — constraints declared `DEFERRABLE
INITIALLY DEFERRED` are checked at commit rather than per statement — but
deferring the check means violations surface only at COMMIT, far from the
statement that caused them, and it does nothing for the surrogate keys or the
validation gaps below; hand-ordering is the default reality.)

**The surrogate-key nests.** What does `(2, 3)` mean? You have to cross-reference
two other statements to find out it grants `report.run` to `controller`. The
diff of a permission change is a diff of integer pairs. Django's fixture system
has surfaced this exact failure mode repeatedly — hardcoded primary keys that
collide and re-insert instead of updating
([ticket #31531](https://code.djangoproject.com/ticket/31531), and downstream
reports like [NetBox #10940](https://github.com/netbox-community/netbox/issues/10940),
where re-running `loaddata` hits duplicate keys).

**Non-idempotent re-runs.** Run the script twice and the second run dies on a
unique violation — unless every statement grows its own `ON CONFLICT` clause,
at which point the script is no longer data anyone can read.

**The schema-evolution tax.** Add a NOT NULL column to `permission` and every
seed INSERT that omits it breaks. The script has to be revised in lockstep with
migrations, forever.

**Nothing validates the data itself.** SQL syntax is checked; content is not. A
grant referencing a permission that no other statement declares is not an error
until a human notices the role doesn't work.

None of this is a fringe observation. Microsoft's Entity Framework team renamed
EF Core's "data seeding" feature to "model managed data" because, in their own
words, the old name *"sets incorrect expectations, as the feature has a number
of limitations and is only appropriate for specific types of data"* — the
documented limits: hardcoded primary keys even when the database would generate
them; data removal when a key changes; and explicit foreign-key values for
every child row ([EF Core docs](https://learn.microsoft.com/en-us/ef/core/modeling/data-seeding);
this describes the `HasData` model-managed path, not EF's runtime `UseSeeding`
hook). When a first-party vendor renames the category to lower expectations,
the category has a problem.

## Reference data wants to be data

Now the same catalog as one JSON document, `seeds/roles.json`:

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

Three properties changed at once. Relationships are expressed by **natural
keys** (`"grants": ["invoice.read", ...]`), so no surrogate id appears anywhere
in the medium — the database remains free to generate them. The document has
**no statement order**, so there is nothing to topologically sort by hand. And a
reviewer can read a permission change in the diff without decoding integer
pairs.

What buys the graph back is a PostgreSQL feature pair that has been stable for
over a decade: `jsonb_to_recordset` (9.4+) turns a JSON array into typed rows,
and [data-modifying CTEs](https://www.postgresql.org/docs/current/queries-with.html)
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
        SET description = EXCLUDED.description
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

One statement, three tables, the whole graph — and the JSON never lands in a
column; `jsonb_to_recordset` parses it into ordinary normalized rows on the way
through. The foreign keys are resolved by joining the `RETURNING` sets on
natural keys — `jsonb_to_recordset` even maps the JSON `grants` array straight
onto a `text[]` column. Run it against an empty catalog: 3 roles, 4 permissions,
6 grants. Run it again: the same counts, the same generated ids, updated
descriptions. Both verified below.

Two mechanics in this statement deserve precision, because they are where
copy-adapted versions go wrong:

**The `RETURNING` chain is load-bearing, not stylistic.** All sub-statements of
a `WITH` execute against the same snapshot — the PostgreSQL documentation is
explicit that they cannot "see" one another's effects on the target tables. A
final INSERT that tried `SELECT role_id FROM role WHERE key = ...` would not
find rows inserted two CTEs earlier *in the same statement*. `RETURNING` is the
sanctioned channel between them, which is exactly why this pattern composes: the
data flows through the query, never through re-reads.

**`DO UPDATE`, not `DO NOTHING`, on the parent tables.** `ON CONFLICT DO
NOTHING` returns no row for a conflict, so on a re-run every already-existing
role and permission silently drops out of the `RETURNING` chain — and their
grants vanish from the final join. I verified this trap directly: against a
catalog where one role and one permission pre-existed, the `DO NOTHING` variant
loaded 3 of 6 grants without any error. The `DO UPDATE` variant returns the row
whether it was inserted or updated, keeps the chain complete, and gets you
idempotent upserts of descriptions for free. (On the leaf table, where nothing
chains further, `DO NOTHING` is exactly right.)

A note on formats, since reference data arrives in more than one: the same
pattern works for XML via
[`xmltable()`](https://www.postgresql.org/docs/current/functions-xml.html) —
worth knowing when the source of truth is an MDM or ERP export — and CSV
handles flat lists well but has no way to express the nested `grants` array
that makes this document a graph.

## Reconciliation: removed rows and drift

Insert-or-update is table stakes. The case none of the tools surveyed below
documents is the row that was *removed* from the seed file but still exists in
the database. Silently ignoring it means the file no longer describes the
catalog. Hard deletion is worse: transactional data references catalog rows,
and a `DELETE` either fails on the foreign keys or — with cascades — takes
history with it.

A defensible default for catalogs is **soft deprecation**:

```sql
WITH doc AS (
    SELECT seed_content::jsonb AS j
),
seeded AS (
    SELECT r.key
    FROM doc, jsonb_to_recordset(doc.j -> 'roles') AS r(key text)
)
UPDATE role
SET deprecated_at = now()
WHERE deprecated_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM seeded s WHERE s.key = role.key);
```

Removed roles stop being assignable but keep their identity, their audit trail,
and their referential integrity. (Note the counterpart in the loader above:
`deprecated_at = NULL` in the role upsert, so a key that returns to the file is
resurrected.) One caveat belongs here: this reconciliation assumes the seed file
owns the whole table. If the application can also insert catalog rows at
runtime, add a provenance column — `managed_by` distinguishing seed-owned from
runtime rows — and scope the reconciliation to seed-owned rows only.

Because the seed document and the live table are queryable side by side, the
same session can also answer a question migration tooling normally cannot:
*has anyone hand-edited the catalog outside the pipeline?*

```sql
WITH doc AS (
    SELECT seed_content::jsonb AS j
),
seed AS (
    SELECT r.key, r.description
    FROM doc, jsonb_to_recordset(doc.j -> 'roles')
         AS r(key text, description text)
)
SELECT r.key, r.description AS live_value, s.description AS seed_value
FROM role r
JOIN seed s USING (key)
WHERE r.description IS DISTINCT FROM s.description;
```

A dozen lines, and every deploy reports drift between the repository's version
of the catalog and production's.

## Seed data gets a test suite

Once the seed file is data in the session, validating it *before any insert* is
a query, not a wish:

```sql
-- every grant must reference a declared permission
WITH doc AS (
    SELECT seed_content::jsonb AS j
),
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

Non-NULL result: raise an exception, abort the deploy, nothing touched. (Note
`NOT EXISTS` rather than `NOT IN` — a single permission entry with a missing
`key` would yield a NULL that silently disarms a `NOT IN` gate; three-valued
logic has no place in a safety check.) The same shape checks duplicate natural
keys (one check per catalog table), missing required fields, malformed values —
this is referential completeness *within the file*, which is precisely the check
per-record schema languages don't do. (JSON Schema is still worth having in CI
for structural linting of the file — types, required fields, key patterns — the
two validations compose rather than compete.)

The second gate runs *after* the load: invariants of the seeded catalog itself.
"Every active role has at least one permission." "Status transitions form a
DAG." "Every plan tier maps to a known feature set." These are ordinary SQL
tests, and if one fails, the deployment — schema changes and seed data together
— should not commit.

## What the ecosystem does today

Fairness requires naming the closest neighbor first. **Liquibase** has the most
capable data facility of the migration tools I surveyed:
[`loadUpdateData`](https://docs.liquibase.com/change-types/load-update-data.html)
loads a CSV with a declared `primaryKey` (composite keys allowed) and performs
insert-or-update against it — genuine idempotent, natural-key seeding, and
credit where due. What it does not give you: the data never becomes queryable
by your deployment logic (no validation queries, no drift report, no
reconciliation of removed rows), the format is flat CSV (the `grants` graph
needs a join-table file with its own key discipline), and environment targeting
is by context tag rather than anything programmable.

**Flyway** approaches data through `${placeholder}` text substitution,
repeatable `R__` migrations, and callbacks such as `afterMigrate`; its own
documentation places idempotency on the author ("It is your responsibility to
ensure the same repeatable migration can be applied multiple times"). A
structural consequence worth knowing: a seed step in an `afterMigrate` callback
runs after the migrations have committed, so a failing seed leaves schema
applied and data half-loaded — the deployment as a whole is not atomic.
**Sqitch** has environment handling via named targets and `--set` variables but
no data-file facility at all. **dbt seed** loads CSVs into tables and draws its
own boundary — the docs recommend against it for large files. All of these are
reasonable tools built on the same assumption: files are either SQL to execute
or CSV to load into a *table*. Of the tools surveyed here, none loads non-SQL
repository files into the deployment session as *queryable data* for the deploy
script itself to consume — which is the one mechanism every technique in this
article depends on. (Newer declarative tools — Atlas, notably — can read
external data files, but as input to migration authoring, not as data the
deploy session itself can query.)

## Running it: the session as the delivery mechanism

Everything above the ecosystem section is plain PostgreSQL — the open question
is how `seeds/roles.json` gets into the session. I maintain
[pgmi](https://github.com/vvka-141/pgmi), a small deployment tool built around
exactly that gap: it loads **every** project file — SQL or not — into
session-scoped views, then executes the `deploy.sql` you write (deploy.sql is
the one file it runs rather than exposes). The seed file arrives as a row:

```sql
SELECT content::jsonb
FROM pg_temp.pgmi_source_view
WHERE path = './seeds/roles.json';
```

So the abstract `WITH doc AS (SELECT seed_content::jsonb ...)` above becomes
that query, verbatim, and the rest of the article runs unchanged. A minimal
project:

```
seed-demo/
├── deploy.sql
├── migrations/001_catalog.sql
├── seeds/roles.json
└── __test__/test_catalog_invariants.sql
```

`deploy.sql` opens a transaction, validates the seed file, applies migrations,
runs the one-statement catalog load, reconciles, prints the drift report, runs
the test suite, and commits — in that order, inside **one** transaction:

```sql
BEGIN;

-- validate the seed file (raises on undeclared grants / duplicate keys)
DO $$ ... $$;

-- apply migrations in path order
DO $$
DECLARE v_file record;
BEGIN
    FOR v_file IN
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- the single-statement graph load, reading from pgmi_source_view
WITH doc AS (
    SELECT content::jsonb AS j
    FROM pg_temp.pgmi_source_view
    WHERE path = './seeds/roles.json'
), ...

SAVEPOINT _tests;
CALL pgmi_test();          -- expands to the __test__/ suite, savepoint-isolated
ROLLBACK TO SAVEPOINT _tests;

COMMIT;
```

![Seed data flow: repository files become queryable session views; deploy.sql validates, migrates, loads, reconciles, and tests inside one transaction — tests pass means COMMIT, any failure rolls everything back](/pgmi/docs/diagrams/a02-seed-data-flow.drawio.svg)

That single `BEGIN ... COMMIT` is what earns the emphasis: **the seed data
commits in the same transaction as the schema migration, gated by the same
tests.** There is no "migrations succeeded, seeds failed halfway" state to
clean up — the failure transcript below shows the whole deployment vanishing
instead. The three scenarios (real output, trimmed to the interesting lines;
exit codes annotated in parentheses):

A clean deploy, and an idempotent re-run:

```
$ pgmi deploy ./seed-demo --connection $DB --force
seed file validated: ./seeds/roles.json
applying ./migrations/001_catalog.sql
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_catalog_invariants.sql
[pgmi] Test suite completed (2 steps)
✓ 3 files loaded, 1 test macro(s) expanded in 0.92s   (exit 0)

$ pgmi deploy ./seed-demo --connection $DB --force    # again
✓ 3 files loaded, 1 test macro(s) expanded in 0.89s   (exit 0)

$ psql $DB -c "SELECT r.key AS role, count(rp.*) AS grants
               FROM role r LEFT JOIN role_permission rp USING (role_id)
               GROUP BY r.key ORDER BY r.key;"
      role       | grants
-----------------+--------
 auditor         |      2
 catalog_manager |      1
 controller      |      3
```

A seed file granting an undeclared permission — caught by the validation query
before any insert:

```
$ pgmi deploy ./seed-demo --connection $DB --force
✗ Failed after 0.87s
pgmi: error: execution failed: ERROR: seed file grants undeclared
  permissions: catalog.publish (SQLSTATE P0001)              (exit 13)

$ psql $DB -c "\dt"
Did not find any relations.
```

And the test gate: a structurally valid seed that violates a catalog invariant
(a role with zero permissions) passes validation, loads — and then the test
fails and takes the *entire* deployment with it, schema included:

```
$ pgmi deploy ./seed-demo --connection $DB --force
seed file validated: ./seeds/roles.json
applying ./migrations/001_catalog.sql
[pgmi] Test: ./__test__/test_catalog_invariants.sql
✗ Failed after 1.42s
pgmi: error: execution failed: ERROR: catalog invariant violated:
  1 active role(s) with no permissions (SQLSTATE P0001)      (exit 13)

$ psql $DB -c "\dt"
Did not find any relations.
```

### The environment layer

The same session carries deploy-time parameters — read from layerable
`.env`-format files (`--params-file`), surfaced in SQL as
`current_setting('pgmi.key', true)` — so environment-conditional seeding
("load `./seeds/dev/` demo datasets only in development") is one `IF` in
deploy.sql, and a CI pipeline can *generate* a seed file with facts only it
knows and drop it next to the static ones; deploy.sql reads it from the same
view.

One boundary belongs in every seeding design: **credentials are not reference
data.** Seeding an *identity* can be legitimate — an anonymous user, a system
principal — but a seeded account with a fixed password is how hardcoded
credentials end up in repositories. The one genuine bootstrap need — an initial
admin — should take its password from a deploy-time parameter delivered via a
params file, never a CLI argument that leaks through argv and shell history.
And a parameter is not a vault: it lives in the session's settings for the
deploy's duration, and a `... PASSWORD` statement can reach the server log
under `log_statement=ddl`. Treat the bootstrap password as disposable —
created by the pipeline, consumed once, rotated.

## Honest limits

**Small catalogs don't need this.** If your reference data is one flat table of
twelve rows, a plain INSERT with `ON CONFLICT` is clearer than any of the above.
The techniques pay off when the data is a graph and the team edits it often.

**This is not a bulk-loading path.** Parameters and file content travel as text
through a session; for genuinely large datasets, PostgreSQL's own guidance is
[`COPY`](https://www.postgresql.org/docs/current/populate.html), and dbt's
documentation draws the same line for its seed feature. Reference catalogs are
hundreds to low thousands of rows; if you are seeding millions, you are doing
ingestion, and ingestion wants different tools.

**One-time seeding is not ingestion, full stop.** Data that arrives
continuously — from a lakehouse, an ETL job, CDC — needs a pipeline with its
own delivery and reconciliation semantics. The boundary is consensus across the
ecosystem: invariant reference data ships with the schema in every environment;
everything else does not belong in the deploy.

**JSON diffs are not free.** A reviewer reads `"grants": ["invoice.read"]`
more easily than `(2, 3)`, but a 400-line JSON document still needs formatting
discipline (one entity per line, sorted keys) to keep diffs reviewable.

**The transactional guarantee has preconditions.** Schema-plus-seed atomicity
holds because everything runs between one `BEGIN` and one `COMMIT` on one
session — which also means a direct connection or session-mode pooler;
transaction-mode poolers recycle backends and break session-scoped state. And
DDL that cannot run in a transaction block (`CREATE INDEX CONCURRENTLY`,
notably) cannot live inside this envelope.

## Takeaway

Reference data fails as SQL scripts for a structural reason: a script encodes
*how* to insert — order, surrogate keys, conflict handling — when what you want
to version is *what should exist*. Move the catalog into a document keyed by
natural identifiers, and PostgreSQL already has the rest: `jsonb_to_recordset`
to type it, data-modifying CTEs to load a whole graph in one statement,
`ON CONFLICT DO UPDATE` to make re-runs safe, and plain queries to validate the
file before loading, reconcile what was removed, and report drift after. The
remaining gap is delivery — getting the file into the session where those
queries can see it — and that is a tooling choice. The semantics are yours
either way: seed data as data, reconciliation over insertion, and a test gate
that refuses to commit a catalog your own invariants reject.

---

## Sources

- Microsoft, [EF Core data seeding](https://learn.microsoft.com/en-us/ef/core/modeling/data-seeding) — the "model managed data" rename and its documented limitations (`HasData` path)
- Liquibase, [`loadUpdateData`](https://docs.liquibase.com/change-types/load-update-data.html) — first-class idempotent CSV loading against a declared primary key
- Redgate, [Flyway documentation](https://documentation.red-gate.com/flyway) — placeholders, repeatable migrations ("It is your responsibility to ensure the same repeatable migration can be applied multiple times"), and callbacks
- Sqitch, [sqitch-deploy manual](https://sqitch.org/docs/manual/sqitch-deploy/) — targets and `--set` variables; no data-file facility
- dbt, [seeds documentation](https://docs.getdbt.com/docs/build/seeds) — CSV loading and its own "not for large files" boundary
- PostgreSQL documentation: [WITH queries §7.8 (data-modifying statements)](https://www.postgresql.org/docs/current/queries-with.html); [JSON functions (`jsonb_to_recordset`)](https://www.postgresql.org/docs/current/functions-json.html); [INSERT ... ON CONFLICT](https://www.postgresql.org/docs/current/sql-insert.html); [`xmltable`](https://www.postgresql.org/docs/current/functions-xml.html); [Populating a database](https://www.postgresql.org/docs/current/populate.html)
- Django ticket [#31531](https://code.djangoproject.com/ticket/31531); [NetBox #10940](https://github.com/netbox-community/netbox/issues/10940) — hardcoded-PK fixture re-run failures in the wild
- [JSON Schema specification](https://json-schema.org/specification) — structural linting of seed files in CI
- All SQL verified on PostgreSQL 17.10; the pgmi transcripts are from real runs of the shown project (a post-v0.11.0 development build of pgmi) against a fresh database — success path, validation-failure path (exit 13, nothing committed), and invariant-failure path (exit 13, schema and seed rolled back together).
