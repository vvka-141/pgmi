---
title: "Your Migration Numbers Are a Distributed Counter Without Coordination"
date: 2026-07-31
author: "Alexey Evlampiev"
description: "Why migration-number collisions are identity, ordering, and enforcement problems — and how pgmi lets SQL validate the exact plan before execution."
weight: 3
---

# Your Migration Numbers Are a Distributed Counter Without Coordination

*What migration-number collisions reveal about identity, ordering, and enforcement.*

*By Alexey Evlampiev*

Two engineers branch from the same commit on the same Tuesday. Each adds a
migration; each picks the next free number — `V42`. Git merges both files
without complaint: two new files, no textual conflict. The migration runner
cannot merge them. Two changes now claim the same position.

Timestamp versions make that exact collision unlikely, but trade it for a
subtler problem: late arrival. A migration stamped earlier can merge *after* a
later one has already run in some environment. Flyway ignores the straggler by
default; its [`outOfOrder`](https://documentation.red-gate.com/fd/flyway-out-of-order-setting-277579015.html)
setting applies it — "a newly discovered version 2.0 will be executed rather
than ignored" — at the cost of an application history that now differs from a
freshly built environment. If you have run migrations on a team
of more than two people you have lived some version of this, and lived the
fixes: timestamp prefixes, `outOfOrder`, renumbering commits, "announce in
Slack before you add a migration."

These are not filename problems. They come from three choices every
ordered-change system makes: how changes acquire **identity**, how **order** is
represented, and where the resulting **invariants** are enforced. Sequential
numbers fuse identity and order into a single uncoordinated allocation — each
branch reads the same prefix of history, independently allocates the next
slot, and git later unions the allocations without checking uniqueness.

pgmi takes a deliberately narrow approach to the third choice. It is **not a
migration framework**: it keeps no applied-migrations ledger by default and
does not decide what has already run. What it does is expose the computed
source plan as an ordinary PostgreSQL relation:

```sql
SELECT execution_order, sort_key, path
FROM pg_temp.pgmi_plan_view
ORDER BY execution_order;
```

That is not diagnostic output rendered from an internal plan object. It *is*
the plan. The same `deploy.sql` that queries those rows is the code that will
execute them — so a project can inspect, reject, transform, or execute the plan
before any source file runs, and, against a disposable database in CI, before
the branch merges.

One scope note. This is the authoring-time problem — two branches allocating
the same position. It is distinct from runtime coordination of concurrent
migrator processes, which tools handle with locks: Flyway and Liquibase
serialize concurrent migrations, and pgmi holds a deploy advisory lock and
exits with a dedicated code when another deploy is already running. This
article is about the first problem.

## The problem is visible across ecosystems

**Liquibase** avoids bare sequence numbers as changeset identity.
[Its FAQ explains why](https://docs.liquibase.com/faq.html): on separate
branches, two people easily pick the same id, and the version-control system
"won't care that there are two different changesets with the same 'id'." The
modern design identifies a changeset by the triple id + author + changelog
path, and [the docs are explicit](https://docs.liquibase.com/concepts/changelogs/changeset.html)
that `id` is an identifier, not an ordering value.

**Redgate's Flyway** [documents the same hazard](https://www.red-gate.com/hub/product-learning/flyway/branching-and-merging-in-database-development-using-flyway/):
"What if a colleague saves a file with the same version number? Flyway will
rightfully protest because the order of execution is now ambiguous." On a
duplicate version Flyway aborts before applying either migration — safe, and
still a merge-day fire drill. The guide's own fixes — timestamp versions,
placeholder files, `outOfOrder`, cherry-picking — it characterizes as
workarounds that "compromise the value of control."

Further down the stack, **golang-migrate users have requested** a
duplicate-version lint ([issue #720](https://github.com/golang-migrate/migrate/issues/720):
"it is common that they generate migrations with the same version number"),
and **Rails'** [v3.2 guide](https://guides.rubyonrails.org/v3.2/migrations.html)
records that pre-2.1 sequential numbers made it "easy for these to clash
requiring you to rollback migrations and renumber them" — the origin of its
2008 switch to timestamps.

## Identity, ordering, enforcement

Every migration system answers three questions, and keeping them separate is
what makes the landscape legible:

1. **Identity** — what makes two changes the same change or different ones?
2. **Ordering** — is order a total ordinal, a timestamp, a declared sequence,
   or a dependency graph?
3. **Enforcement** — what invariant is checked, when, and by whom?

The first two are properties of the data model. The third is a property of
workflow: the same duplicate Flyway version can surface in a local `validate`,
in pre-merge CI, or at deploy time, depending on how the team wires it.

| System | Change identity | Order representation | Enforcement surface |
|---|---|---|---|
| Flyway | Unique versioned script | Total version order | `validate` / `migrate` |
| Liquibase | id + author + path | Changelog sequence | Changelog validation / update |
| Alembic | Revision identifier | Dependency DAG, topologically sorted | Head / merge validation |
| Sqitch | Named change + SHA-1 change ID | Declared order + `--requires` edges | `verify` / plan review |
| Atlas | Versioned script | Linear sequence | Generated `atlas.sum` + CI |
| pgmi | Session row keyed by path; optional declared UUID | Computed sort-key/path relation | Project SQL over the plan; no built-in ledger |

Four rows carry footnotes. **Timestamp versions** — Rails and golang-migrate's
CLI default are examples — make duplicate allocation rare rather than
impossible, and they leave the late-arrival question unanswered: what happens
when an earlier-stamped change merges after a later one has already run? The
answer depends on the tool's history model — Flyway ignores it by default and
applies it late only when `outOfOrder` is enabled. A **centralized plan file** — Liquibase's root changelog where teams use one,
Sqitch's `sqitch.plan` — moves ordering changes into a single review surface:
concurrent insertions near the same position usually conflict in git, and even
a clean merge leaves the resulting order visible in one diff. Sqitch treats
order as declared data with `--requires` dependency edges and predates pgmi —
order as data is not a pgmi invention. **Alembic's
[DAG](https://alembic.sqlalchemy.org/en/latest/branches.html)** models branch
divergence explicitly: revisions declare dependencies, valid orders come from a
topological sort, and `alembic merge` reconciles heads with a merge node —
though a team still has to notice multiple heads and target or merge them
deliberately. **[Atlas's `atlas.sum`](https://atlasgo.io/concepts/migration-directory-integrity)**
is not an ordering model at all but an enforcement mechanism over a linear one:
adding a file on any branch rewrites the generated sum-file, forcing an
integrity conflict in most VCS workflows and a CI failure — a genuinely good
idea.

So where is pgmi actually different? Not in a new *shape* of order — declarative
order, exposed plans, and programmable validation all exist elsewhere, through
plan files, plugins, callbacks, and CI. The precise distinction is this: **pgmi
exposes its computed plan as a PostgreSQL relation inside the same session in
which `deploy.sql` validates and executes it.** The policy language is SQL, and
the assertion reads the runner's actual input rather than reconstructing the
plan in a separate tool.

## Reject an unintended plan before it runs

pgmi does **not** stop two authors from choosing the same sort key. If two
branches both claim `200/010`, the view still orders the files
deterministically — same result on every server for a fixed database encoding,
by a path tie-break — but deterministic is not intentional. For independent
files the tie-break may be exactly right; for dependent ones a silent tie-break
is more dangerous than Flyway's loud duplicate-version error. The query model
changes *where that policy is expressed*: "no two plan rows may claim the same
position" is a `GROUP BY` over the same relation the deploy will execute — under
the same `COLLATE "C"` discipline the view itself uses, so grouping never
depends on the server's collation:

```sql
DO $$
DECLARE
    v_collisions TEXT;
BEGIN
    SELECT string_agg(
               format('%s: %s', sort_key, paths),
               E'\n' ORDER BY sort_key COLLATE "C"
           )
      INTO v_collisions
      FROM (
          SELECT
              sort_key COLLATE "C" AS sort_key,
              string_agg(path, ', ' ORDER BY path COLLATE "C") AS paths
          FROM pg_temp.pgmi_plan_view
          GROUP BY sort_key COLLATE "C"
          HAVING count(*) > 1
      ) AS collision;

    IF v_collisions IS NOT NULL THEN
        RAISE EXCEPTION E'duplicate plan positions:\n%', v_collisions
            USING HINT = 'Assign each file a distinct sort key, or drop this check if ties are intentional.';
    END IF;
END $$;
```

A filesystem linter could also flag duplicate keys. The difference is what it
would have to duplicate: pgmi's metadata parsing, its path-fallback rule, its
multi-key expansion, and its `COLLATE "C"` tie-breaking. `deploy.sql` queries
the exact relation it will then execute, so validation and execution cannot
silently disagree about what the plan contains. The advantage is not that SQL
can find duplicates; it is that the check reads the runner's own input.

The demo project is a phased catalog load — schema, an integrity check, bulk
seed files, then indexes and a validated foreign key. It follows
[PostgreSQL's recommendation](https://www.postgresql.org/docs/current/populate.html)
to load before adding indexes and validating foreign keys; the intermediate
check is the demo's own addition. Replaying the opening: one branch adds
`load/010_products.sql` at `200/010`, another adds `load/012_categories.sql`
and reuses the key. The merge is clean; the deploy is not. Real transcript,
PostgreSQL 16, password redacted:

```
$ pgmi deploy . --connection "postgresql://postgres:...@127.0.0.1:5440/postgres" -d catalog_demo
Preparing session: scanning files, loading parameters
Loaded 6 files
Executing deploy.sql
deployment plan:
  #1 | 100/010 | ./schema/010_catalog.sql
  #2 | 150/000 | ./checks/smoke.sql
  #3 | 200/010 | ./load/010_products.sql
  #4 | 200/010 | ./load/012_categories.sql
  #5 | 200/020 | ./load/020_prices.sql
  #6 | 300/010 | ./post/010_indexes.sql
  #7 | 400/000 | ./checks/smoke.sql
✗ catalog_demo: failed after 0.13s
pgmi: error: execution failed: ERROR: duplicate plan positions:
200/010: ./load/010_products.sql, ./load/012_categories.sql (SQLSTATE P0001)
HINT: Assign each file a distinct sort key, or drop this check if ties are intentional.
WHERE: PL/pgSQL function inline_code_block line 20 at RAISE
```

Exit code 13; no planned source file executed — the plan printer and the
assertion ran, which is the point. The printed plan shows what pgmi does on its
own (#3 and #4, tied key, path tie-break); the exception shows what the project
decided about it. A project whose seed files are genuinely unordered peers
deletes the check; a project migrating dependent schema keeps it strict.

## The same policy, before merge

A fair objection: "you replaced Flyway's deploy-time error with a PL/pgSQL
deploy-time error." But the assertion lives in `deploy.sql`, and does not have
to wait for a production deploy. A pgmi deployment is a self-contained session
against whatever database you point it at — so point it at a disposable one in
pull-request CI. The exact policy that guards production then rejects an
unreviewed ordering change during review, and runs again at deploy as defense
in depth.

This is not hypothetical: the repository's CI job does precisely this against
an ephemeral PostgreSQL service. Stripped to its two essential commands
— the full [`example-execution-order` job](https://github.com/vvka-141/pgmi/blob/main/.github/workflows/examples.yml)
adds checkout, Go build, service wiring, and cleanup:

```bash
pgmi deploy . -d catalog_demo --force       # success path: must exit 0

cp ../break-it/012_categories.sql load/
pgmi deploy . -d catalog_demo --force       # duplicate sort key: must exit 13
```

The causal loop the opening scenario broke is now closed at the merge boundary:

```
branch allocates a position
  → merge candidate
  → ephemeral PostgreSQL session in CI
  → plan assertion
  → reviewed fix, or rejected PR
  → production deploy (same assertion, defense in depth)
```

The collision still happens — nothing about a query prevents two branches from
wanting the same slot. What changed is where it surfaces (before merge) and who
defined the rule (the project, in SQL it can read and amend).

## How the plan is computed

The mechanism is one query, abridged here from pgmi's
[session contract](https://vvka-141.github.io/pgmi/docs/session-api/):

```sql
SELECT
    s.path,
    unnested.sort_key,
    ROW_NUMBER() OVER (
        ORDER BY unnested.sort_key COLLATE "C", s.path COLLATE "C"
    ) AS execution_order
FROM pg_temp._pgmi_source s
LEFT JOIN pg_temp._pgmi_source_metadata m ON s.path = m.path
CROSS JOIN LATERAL UNNEST(
    COALESCE(NULLIF(m.sort_keys, '{}'), ARRAY[s.path])
) AS unnested(sort_key);
```

Ordering is a spectrum, not a mandate. With no metadata, the fallback
`ARRAY[s.path]` gives lexicographic path order — the zero-ceremony default. A
file that wants an explicit position declares sort keys in a `<pgmi-meta>`
comment block. When neither fits, `deploy.sql` can skip the plan view and
impose any order SQL can express over `pgmi_source_view` — a `CASE` phase
ladder, a join against a manifest table. The XML metadata is the declarative
option, never the mechanism; the mechanism is the query.

One detail carries a transferable lesson. Both sort expressions specify
`COLLATE "C"` — byte-value order, in
[PostgreSQL's definition](https://www.postgresql.org/docs/current/collation.html) —
because `sort_key` mixes user keys (`100/010`) with path fallbacks
(`./load/010_products.sql`), and a linguistic collation can order the two
groups differently depending on locale, provider, and version. `COLLATE "C"`
removes that variable: for a fixed database encoding — normally UTF-8 — the
same keys receive the same order across servers, and pgmi pins that with a
contract test and an integration test. PostgreSQL's fine print stands —
C-collation order is a function of the encoding and can differ *between*
encodings — but it no longer depends on the server's locale, configuration
deployment authors may not control. If your own tooling sorts anything, this
failure mode applies to it too.

## Two stricter patterns, and their cost

The collision gate rejects one specific hazard. Two heavier patterns build on
the same relation; both are opt-in, and pgmi requires neither.

**Pin the whole plan.** A project can diff the computed plan against a
hand-written manifest, so *any* reordering — new file, changed key, renamed
path — must be acknowledged in the commit that causes it. The cost comes first:
the manifest duplicates the intended sequence, and because the check
`FULL JOIN`s on `execution_order`, one insertion produces one diff line per
displaced row — noisy by design, because it compares positions, not set
membership. When a branch adds `load/015_discounts.sql` at `200/015` — a
reasonable file in a reasonable position — without updating the manifest, that
is exactly what you see:

```
✗ catalog_demo: failed after 0.12s
pgmi: error: execution failed: ERROR: plan does not match the reviewed manifest:
#4: manifest has (200/020, ./load/020_prices.sql), plan has (200/015, ./load/015_discounts.sql)
#5: manifest has (300/010, ./post/010_indexes.sql), plan has (200/020, ./load/020_prices.sql)
#6: manifest has (400/000, ./checks/smoke.sql), plan has (300/010, ./post/010_indexes.sql)
#7: not in manifest — plan has (400/000, ./checks/smoke.sql) (SQLSTATE P0001)
HINT: Review the change, then update the manifest in deploy.sql.
```

The instinct is the same as Atlas's sum-file: convert a silent ordering change
into a loud failure. The honest difference is that Atlas generates its
checksum, while this manifest is hand-written — worth its maintenance only where
execution order is critical enough to review. The full assertion, and a looser
set-membership variant with more compact output, are in the runnable example's
[`deploy.sql`](https://github.com/vvka-141/pgmi/blob/main/examples/execution-order-policy/project/deploy.sql).

**One source file, several plan positions.** Because the plan `UNNEST`s sort
keys, a file with N keys becomes N plan rows. The demo's integrity check,
`checks/smoke.sql`, declares `150/000` and `400/000` and runs at positions #2
and #6 — once against the empty schema, once against the loaded catalog. In
Flyway and Sqitch, analogous reuse normally goes through indirection: a
positioned wrapper calling a shared procedure, Flyway's repeatable migrations
or callbacks, or [Sqitch's `rework`](https://sqitch.org/docs/manual/sqitch-rework/),
which physically copies the script. Two caveats: each position re-runs the
*entire file*, so a multi-positioned file must be idempotent; and the classic
pre/post pairs (drop/recreate index, `NOT VALID`/`VALIDATE`) are different SQL —
two ordinary files in any tool. Multi-positioning earns its keep only where the
halves are the same bytes.

![Five source scripts expand to six plan rows; ./checks/smoke.sql carries two sort keys and lands at positions #2 and #6](/pgmi/docs/diagrams/a03-execution-order-plan.drawio.svg)

## Limits

What this model does not give you:

- **Policy evaluation needs a PostgreSQL session.** The assertions run inside
  the deploy — pgmi provides no built-in static, database-free plan-policy
  check. That is the price of reading the runner's actual input, and the reason
  the pre-merge check spins up a disposable database.
- **Sort keys are lexicographic strings, not a dependency graph.** pgmi does no
  topological sorting; `100/010` runs before `200/010` because of byte order,
  and that is all. If your project needs "B after A" as an edge, Alembic's and
  Django's DAGs — or Sqitch's `--requires` — model that directly.
- **No built-in applied-migrations ledger.** Every deployment computes a fresh
  plan; nothing records what ran last time unless `deploy.sql` writes a tracking
  table. The metadata UUID can key that history, while `idempotent` tells
  project SQL whether a source should run again or be skipped. Tools with a
  ledger give you skip-already-applied for free.
- **Policy you don't write doesn't exist.** Out of the box, pgmi's only
  collision behavior is the deterministic tie-break. The gate and the manifest
  are patterns your project adopts — the point, and the cost.
- **The plan is session-scoped.** It lives in `pg_temp` during the deploy.
  Auditing means persisting it yourself (one `INSERT … SELECT FROM
  pgmi_plan_view`).
- **PostgreSQL only, SQL required.** If your team wants framework-managed linear
  migrations without writing deployment-control SQL, Flyway and golang-migrate
  are simpler tools for that job, and Liquibase handles multi-database estates
  pgmi does not attempt.

## Closing

Identity, ordering, enforcement — the split makes a usable checklist for any
migration system, pgmi included:

1. Can two branches allocate the same identity?
2. Does order represent time, dependency, or policy?
3. What happens to a late-arriving change?
4. Which invariant is checked before execution — and can it also run before
   merge?
5. Can two environments legally observe different applied orders?

pgmi's answers: source rows are keyed by path, with an optional declared
metadata UUID for project-defined tracking; order is a query result; and the
pre-execution invariant is whatever assertion the project writes — in
production and, against a disposable database, in pre-merge CI. Once execution
order is the same relation the deployment consumes, ordering policy becomes
reviewable program logic rather than hidden runner behavior or a convention
reconstructed by separate tooling. That is both the freedom and the obligation
of the model.

The runnable project behind every transcript lives at
[`examples/execution-order-policy`](https://github.com/vvka-141/pgmi/tree/main/examples/execution-order-policy)
— five source scripts plus a `deploy.sql`, two break-it variants, and the CI
job shown above. The [session API contract](https://vvka-141.github.io/pgmi/docs/session-api/)
documents `pgmi_plan_view`; the [metadata reference](https://vvka-141.github.io/pgmi/docs/metadata/)
covers `<pgmi-meta>` and sort keys.


## Sources

- Liquibase: [FAQ](https://docs.liquibase.com/faq.html) · [changeset concept](https://docs.liquibase.com/concepts/changelogs/changeset.html)
- Flyway/Redgate: [branching & merging guide](https://www.red-gate.com/hub/product-learning/flyway/branching-and-merging-in-database-development-using-flyway/) · [outOfOrder setting](https://documentation.red-gate.com/fd/flyway-out-of-order-setting-277579015.html) · [repeatable migrations](https://documentation.red-gate.com/fd/repeatable-migrations-273973335.html)
- golang-migrate: [issue #720](https://github.com/golang-migrate/migrate/issues/720)
- Rails: [v3.2 migrations guide](https://guides.rubyonrails.org/v3.2/migrations.html) (schema-versioning history)
- Alembic: [Working with Branches](https://alembic.sqlalchemy.org/en/latest/branches.html) · Django: [Migrations](https://docs.djangoproject.com/en/stable/topics/migrations/)
- Sqitch: [the plan file](https://sqitch.org/docs/manual/sqitch-plan/) · [`rework`](https://sqitch.org/docs/manual/sqitch-rework/)
- Atlas: [migration directory integrity](https://atlasgo.io/concepts/migration-directory-integrity)
- PostgreSQL: [Populating a Database](https://www.postgresql.org/docs/current/populate.html) · [Collation Support](https://www.postgresql.org/docs/current/collation.html)
- pgmi internals: [`internal/contract/api-v1.sql`](https://github.com/vvka-141/pgmi/blob/main/internal/contract/api-v1.sql) (pgmi_plan_view) · [`internal/params/schema.sql`](https://github.com/vvka-141/pgmi/blob/main/internal/params/schema.sql) (sort_keys) · [`contract_test.go`](https://github.com/vvka-141/pgmi/blob/main/internal/contract/contract_test.go), [`deployer_plan_order_test.go`](https://github.com/vvka-141/pgmi/blob/main/internal/services/deployer_plan_order_test.go) (COLLATE "C" enforcement)
