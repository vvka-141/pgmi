---
title: "Test PostgreSQL migrations before COMMIT"
date: 2026-07-11
author: "Alexey Evlampiev"
description: "PostgreSQL's transactional DDL lets you run assertions against the migrated schema inside the deployment transaction — so a failing check means the deployment never happened."
weight: 20
---

# Test PostgreSQL migrations before COMMIT

*Test a migration after it applies but before it commits — and let `COMMIT` depend on the checks.*

*By Alexey Evlampiev*

Most migration pipelines test before deployment or after commit. PostgreSQL
allows a third checkpoint: **after applying the change, but before committing
it**. Lint and rehearsal catch what they can in advance; integration suites
catch what they can afterwards. The checkpoint in between is the only one
where the real target database — with whatever drift it has accumulated — is
in its migrated state while rollback is still one statement away.

PostgreSQL supports this with no framework at all: run the migration **and**
the checks that prove it worked inside one transaction, and let `COMMIT`
depend on the checks. If an assertion fails, the deployment doesn't fail
*partially* — within PostgreSQL's transactional scope, it never happened.

The pattern needs no framework: first we'll build it with plain SQL and psql,
then look at where its limits are and how to automate it across a project.

## The foundation: DDL you can roll back

PostgreSQL's documentation defines transactions as
[all-or-nothing operations](https://www.postgresql.org/docs/current/tutorial-transactions.html)
and documents savepoints for selectively discarding part of a transaction.
What makes the deployment story unusual is that PostgreSQL's transactional
treatment extends to most DDL: `CREATE TABLE`, `ALTER TABLE`,
`CREATE FUNCTION`, and index creation obey `BEGIN` and `ROLLBACK` like
ordinary writes. The [PostgreSQL wiki's competitive
analysis](https://wiki.postgresql.org/wiki/Transactional_DDL_in_PostgreSQL:_A_Competitive_Analysis)
compares this across engines; the short version is that a failed migration on
MySQL can leave you half-migrated (DDL there commits implicitly), while
PostgreSQL rolls the transaction's schema and data changes back together.

Most migration tools use this property defensively: if a statement errors,
the migration rolls back. That's good, but it only protects you from
migrations that *fail loudly*. The more expensive failures are the ones that
succeed syntactically and are wrong semantically — the backfill that missed
rows, the function that returns the wrong shape, the constraint that was
created `NOT VALID` and never validated.

Transactional DDL supports something stronger than error rollback: it lets
you make **your own checks** part of the transaction.

## The pattern in plain psql

Suppose a deployment adds a `region` column, backfills it from a mapping
table, and makes it mandatory. The interesting question isn't "did the
statements run" — the DDL itself will complain if it can't. The question is
one the new `NOT NULL` constraint cannot answer: **did every customer get
the region its country implies?** A backfill can write a wrong-but-non-null
value — an ambiguous mapping, a join that silently matched the wrong rows —
and every statement still succeeds. Ask the real question *before*
committing:

```sql
-- deploy.sql
BEGIN;

ALTER TABLE customer ADD COLUMN region text;

UPDATE customer c
SET    region = m.region
FROM   region_mapping m
WHERE  m.country = c.country;

-- The gate: verify the migrated state inside the same transaction.
DO $$
DECLARE
    v_unmapped   bigint;
    v_mismatched bigint;
BEGIN
    SELECT count(*) INTO v_unmapped
    FROM customer
    WHERE region IS NULL;

    SELECT count(*) INTO v_mismatched
    FROM customer c
    JOIN region_mapping m ON m.country = c.country
    WHERE c.region IS DISTINCT FROM m.region;

    IF v_unmapped > 0 OR v_mismatched > 0 THEN
        RAISE EXCEPTION
            'region backfill invalid: % unmapped, % mismatched',
            v_unmapped, v_mismatched;
    END IF;
END $$;

ALTER TABLE customer ALTER COLUMN region SET NOT NULL;

COMMIT;
```

The two checks earn their place differently. The unmapped count fires before
`SET NOT NULL` would, turning a generic constraint error into a diagnostic
with numbers in it. The mismatch count is the real gate: it catches a
wrong-but-non-null backfill that `NOT NULL` happily accepts. A persistent
relational constraint — a composite foreign key onto the mapping table —
could enforce this relationship continuously, and where that fits your
model it is the stronger tool; the deployment gate earns its keep when such
a constraint is absent, impractical, or when you want a diagnostic count
*before* introducing it. And because the assertion describes the intended
state rather than the backfill implementation, it remains valid if the
backfill is rewritten before deployment or reused in a later migration —
though unlike a constraint, it protects only deployments in which it
actually runs.

Run it with psql configured to stop on the first error:

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f deploy.sql
```

If the `RAISE EXCEPTION` fires, psql exits non-zero, the open transaction
aborts, and the connection closes — PostgreSQL rolls back the transaction's
schema and data changes: the `ALTER TABLE`, the backfill, all of it. Your
pipeline sees a non-zero exit code and stops. (Non-transactional effects are
the subject of the limitations section below.)

Notice what did the work here: not a testing framework, not a migration tool
— one `DO` block and PostgreSQL's transaction semantics. `RAISE EXCEPTION` is
a perfectly good assertion primitive.

## Test data that never persists: savepoints

Assertions that only read are easy. Real verification often needs to *write*
— insert a probe row, exercise an upsert function, check a trigger fired.
You want those writes gone before `COMMIT`, while keeping the migration's
changes.

That's what savepoints are for:

```sql
-- ...migration statements above...

SAVEPOINT tests;

DO $$
BEGIN
    -- New rows must carry a region now.
    BEGIN
        INSERT INTO customer (email, country) VALUES ('probe@example.com', 'NL');
        RAISE EXCEPTION 'region NOT NULL is not enforced';
    EXCEPTION
        WHEN not_null_violation THEN
            NULL;  -- exactly what we wanted
    END;

    -- And rows that do carry one still insert cleanly.
    INSERT INTO customer (email, country, region)
    VALUES ('probe@example.com', 'NL', 'eu');
END $$;

ROLLBACK TO SAVEPOINT tests;   -- probe rows gone, migration intact

COMMIT;
```

The probe rows exist long enough to prove the constraint holds in both
directions, then `ROLLBACK TO SAVEPOINT` discards them — without touching
the migration statements before the savepoint. If either assertion raises,
control never reaches `COMMIT` and the whole transaction aborts. (The inner
`BEGIN ... EXCEPTION` block is how PL/pgSQL traps the *expected* failure —
it gives you an implicit savepoint scoped to the block.)

One wrinkle worth knowing: `SAVEPOINT` and `ROLLBACK TO SAVEPOINT` are
top-level SQL commands. You cannot issue them from inside a PL/pgSQL block —
a `DO` block runs *within* the caller's transaction and gets implicit
savepoints only through `BEGIN ... EXCEPTION` sub-blocks. So the pattern
interleaves top-level savepoint commands with `DO` blocks for the logic, as
above. With one savepoint per test you get suites where every test starts
from clean fixture state regardless of what the previous test did.

Two caveats that keep this honest: sequences are non-transactional, so
`nextval()` advances survive the rollback (harmless gaps, but not
byte-for-byte identity for sequence state); and anything a test does outside
the transaction — `CREATE INDEX CONCURRENTLY`, a dblink call — is not
covered by the savepoint.

## What this gate cannot do

The pattern has hard edges, and they are PostgreSQL's, not any tool's. Expert
readers will already be composing the objections, so here they are up front.

**Some statements refuse to run in a transaction block.**
[`CREATE INDEX CONCURRENTLY`](https://www.postgresql.org/docs/current/sql-createindex.html),
[`VACUUM`](https://www.postgresql.org/docs/current/sql-vacuum.html),
[`ALTER SYSTEM`](https://www.postgresql.org/docs/current/sql-altersystem.html),
`CREATE DATABASE`, `CREATE TABLESPACE`. Anything on that list belongs in a
separate phase, before or after the gated transaction, with its own
verification strategy. And there are subtler cases:
[`ALTER TYPE ... ADD VALUE`](https://www.postgresql.org/docs/current/sql-altertype.html)
runs inside a transaction on modern PostgreSQL (older releases prohibited it
entirely), but the new enum value can't be *used* until after commit — so a
test that inserts a row with the new value fails even though the migration
is correct.

**The transaction holds its locks until the end — tests included.** Most
`ALTER TABLE` forms take `ACCESS EXCLUSIVE`, and a *waiting*
`ACCESS EXCLUSIVE` request queues every later query behind it — the
[lock-queue problem](https://xata.io/blog/migrations-and-exclusive-locks)
that [depesz](https://www.depesz.com/2024/12/12/how-to-alter-tables-without-breaking-application/),
[postgres.ai](https://postgres.ai/blog/20210923-zero-downtime-postgres-schema-migrations-lock-timeout-and-retries),
and [pganalyze](https://pganalyze.com/blog/5mins-postgres-migrations-avoid-deadlocks)
have all written about. Putting verification inside the transaction
*lengthens* the hold. The consequences are practical, not fatal: keep the
in-transaction checks to seconds, set an aggressive `lock_timeout` so the
deploy fails fast instead of stalling production traffic, and do not run
your full integration suite here. The gate is for deployment invariants —
"the backfill is complete", "the function returns the right shape" — not for
everything your CI runs.

**A gate is not a rehearsal.** Testing the migration against a copy — a
[Neon branch](https://neon.com/docs/introduction/branching), a
[Supabase preview branch](https://supabase.com/docs/guides/deployment/branching),
a [Testcontainers](https://testcontainers.com/modules/postgresql/) instance
in CI — catches problems earlier and at leisure, and static analyzers like
[squawk](https://squawkhq.com/docs/safe_migrations) or
[strong_migrations](https://github.com/ankane/strong_migrations) catch unsafe
patterns before anything runs at all. Those practices test the migration
*before the deploy, against a copy*. The in-transaction gate tests the real
target state after applying the migration but before commit — the point
where production drift is visible while transactional rollback is still
available. Three legs: lint, rehearse, gate. They compose; none replaces
another.

## Where existing tools stand

Most migration tools can get *close* to this, and it's worth being precise
about how close.

[Flyway](https://documentation.red-gate.com/fd/migration-transaction-handling-273973399.html)
wraps each migration script in its own transaction by default (and all
pending ones with `group=true`), so an assertion you write inside a migration
file does abort that script's transaction. The verification is just another
statement in a versioned script, and with default settings a failure rolls
back the current script, not the already-committed ones before it.
[Liquibase](https://docs.liquibase.com/concepts/changelogs/attributes/run-in-transaction.html)
runs each changeset in its own transaction; its preconditions are primarily
pre-flight decisions about whether a changeset runs. A PostgreSQL-specific
SQL changeset can still embed a raising assertion after its DDL — but that's
ordinary SQL inside the changeset, not a dedicated post-change verification
model.
[Sqitch](https://sqitch.org/docs/manual/sqitch-deploy/) took verification
more seriously than anyone: every change can carry a dedicated verify
script. But a verify failure is handled by running the change's *revert*
script — a compensating action, rather than a rollback of a still-open
deployment transaction.
[Atlas](https://atlasgo.io/versioned/apply) applies files transactionally
(`--tx-mode all` puts the whole batch in one transaction, where a raising
statement aborts everything — the same trick this article teaches) and adds
static lint; [golang-migrate](https://github.com/golang-migrate/migrate)
generally leaves transaction handling to migration authors and records a
*dirty* version after a failed migration, stopping further migrations
because the resulting database state may be partially applied or otherwise
uncertain — exactly the uncertainty transactional deployment avoids. [pgroll](https://github.com/xataio/pgroll) is solving a
different axis entirely — zero-downtime expand/contract over long-lived,
deliberately non-atomic migrations — and complements rather than competes
with a commit gate.

The honest summary: the building blocks are widely available, but in
framework-managed tools the transaction boundaries and the verification
hooks are the framework's concepts, shaped by its model. If you want the
deployment transaction and its gate to be *one program you write* —
migrations, data loads, probes, savepoints, and the commit decision in a
single control flow — you are writing SQL, and the tool's job shrinks to
getting your files into the session.

## Scaling it: the whole deployment as one SQL program

That's the design of [pgmi](https://github.com/vvka-141/pgmi), a small
deployment tool I built. It loads your project files into session-scoped
temporary tables and executes the `deploy.sql` *you* write, which queries
those files and orchestrates everything — including the test gate:

![Test-gated deployment: apply files, test the changed database, commit only if tests pass — otherwise rollback](https://vvka-141.github.io/pgmi/docs/diagrams/d00-test-gated-deploy.drawio.png)

Tests live in `__test__/` directories next to migrations. A
`CALL pgmi_test()` macro in deploy.sql expands — before the SQL reaches
PostgreSQL — into exactly the top-level savepoint stream described above:
fixture, `SAVEPOINT` per test, execute, `ROLLBACK TO SAVEPOINT`. A failing
test raises, the transaction aborts, exit code 13.

The [runnable example](https://github.com/vvka-141/pgmi/tree/main/examples/test-gated-deploy)
deploys migrations and a test suite in one transaction (output abridged):

```text
Loaded 9 files
Executing deploy.sql
[pgmi] Test suite started
[pgmi] Fixture: ./__test__/_setup.sql
[pgmi] Test: ./__test__/test_user_crud.sql
[pgmi] Test suite completed (3 steps)
✓ 9 files loaded, 1 test macro(s) expanded in 5.21s
```

Copy in the deliberately failing test — it expects an `audit_log` entry that
nothing creates — and deploy to a fresh database:

```text
[pgmi] Test: ./__test__/test_audit_log.sql
✗ Failed after 1.99s — see error above
pgmi: error: execution failed: ERROR: audit_log must contain a deploy event (SQLSTATE P0001)
```

Exit code 13, and the proof that matters:

```sql
SELECT to_regclass('public.audit_log') IS NULL AS rolled_back;
-- t
```

The `audit_log` table from the new migration does not exist. The migration
ran, the test failed, and PostgreSQL rolled both back together. The project's
CI [runs both paths on every push](https://github.com/vvka-141/pgmi/blob/main/.github/workflows/ci.yml)
— the success case must exit 0, the broken case must exit 13 and leave no
trace — so the claim above is continuously verified rather than asserted.

To be clear about what pgmi does *not* do: it doesn't impose the gate. The
transaction boundaries live in your deploy.sql; `CALL pgmi_test()` gates the
commit because you placed it before `COMMIT`. And everything in the
limitations section applies unchanged — concurrent index builds still need
their own phase, and the deployment session needs a direct connection
(transaction-mode poolers recycle backends between statements, which breaks
session-scoped state).

## Takeaway

If you deploy to PostgreSQL, you already own the machinery in this article:
transactional DDL, `DO` blocks, `RAISE EXCEPTION`, savepoints, and
`ON_ERROR_STOP`. Put your deployment invariants inside the deployment
transaction, and "the migration succeeded" stops meaning "the statements
didn't error" and starts meaning "the database provably reached the state we
intended — or is untouched."

Start with one assertion after your next risky backfill. The rest of the
pattern grows from there.

---

*Alexey Evlampiev builds data platforms on PostgreSQL.
[pgmi](https://vvka-141.github.io/pgmi/) is MPL-2.0 open source.*
