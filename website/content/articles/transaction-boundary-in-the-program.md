---
title: "Your Transaction Boundary Belongs in the Program, Not the Filename"
date: 2026-08-01
author: "Alexey Evlampiev"
description: "How migration tools represent non-transactional execution — and how pgmi keeps a phased deployment's transaction boundaries inside deploy.sql."
weight: 5
---

# Your Transaction Boundary Belongs in the Program, Not the Filename

*A phased schema change spans transactional and non-transactional work. Most migration frameworks express that distinction as metadata; pgmi makes it part of the SQL program.*

*By Alexey Evlampiev*

Three facts about PostgreSQL decide how a deployment has to be shaped, and all
three are the server's rules rather than any tool's.

A table-level lock is held until the end of the transaction that took it, not
until the statement finishes — so a six-millisecond `ALTER TABLE` at the top of
a five-minute transaction blocks readers for five minutes. `lock_timeout`
bounds how long a statement *waits* for a lock, which is a different quantity
again. And `CREATE INDEX CONCURRENTLY`, the statement you reach for precisely
because it does not block reads or writes, refuses to run inside a transaction
block at all — and refuses a second way, from inside any function, procedure or
`DO` block, with a different error:

```
DO $$ BEGIN EXECUTE 'CREATE INDEX CONCURRENTLY idx_a ON orders (customer_id)'; END $$;
ERROR:  CREATE INDEX CONCURRENTLY cannot be executed from a function

SELECT 1; CREATE INDEX CONCURRENTLY idx_b ON orders (customer_id);
ERROR:  CREATE INDEX CONCURRENTLY cannot run inside a transaction block
```

The second error is the one that shapes tool design: a multi-statement query is
an implicit transaction block, so "put the concurrent index after the `COMMIT`"
fails whenever the script reaches the server in one message.

Why those rules matter operationally — and how a waiting lock request blocks
every reader that arrives behind it — is the subject of the companion article,
[Your ALTER TABLE Is Fast. The Queue Behind It Is Not.](https://vvka-141.github.io/pgmi/articles/lock-queue-fast-is-not-safe/)
This one takes them as given and asks a narrower question: a single schema
change needs several phases with different transaction requirements, so where
does a deployment say so?

## Where tools put the phase boundary

Any PostgreSQL deployment tool that supports both transactional DDL and
statements like `CREATE INDEX CONCURRENTLY` has to solve the same problem: most
of a deployment wants to be in a transaction, and a few statements must not be. The solutions are remarkably
consistent in shape.

| Tool | How a non-transactional statement is expressed | Unit it attaches to |
|---|---|---|
| Flyway | `executeInTransaction=false`, in project or per-script configuration | migration script |
| Liquibase | `runInTransaction=false` attribute | changeset |
| goose | `-- +goose NO TRANSACTION` annotation | migration file |
| golang-migrate | leave multi-statement mode off, give the statement its own migration | migration file |

golang-migrate's row is the one people misread: `x-multi-statement` is *not* a
"run outside a transaction" switch. Its PostgreSQL README says that "running
multiple SQL statements in one `Exec` executes them inside a transaction," and
directs you to put `CREATE INDEX CONCURRENTLY` in its own migration *without*
multi-statement mode.

The pattern is not "everything is a file" — Liquibase's unit is a changeset,
and several can share one changelog. The pattern is that transaction behaviour
is **metadata attached to a migration unit**, declared outside the SQL, in a
vocabulary belonging to the tool. That is a coherent design, and for a project
whose migrations are one statement each it is barely a constraint. It becomes
one when a single logical change wants four phases with different transaction
needs: the phases have to become separate units, marked separately, sequenced
by name.

Liquibase documents a consequence worth quoting to anyone reaching for the
flag: with `runInTransaction=false`, a failure mid-changeset can leave
`DATABASECHANGELOG` in an invalid state. Turning off the transaction turns off
the safety net too. golang-migrate records the same hazard explicitly, marking
the schema version *dirty* so the next run refuses to proceed. goose leaves
earlier statements applied with the migration itself unrecorded — the same
recovery problem, without a named state to detect it by.

## The same playbook as one program

The PostgreSQL playbook is settled, then. The open question is only where those
choices get written down.

pgmi does not decide where your transaction boundaries belong. It has an
opinion about where that decision should live — in deploy.sql, beside the
statements whose safety depends on it — and the whole contract is one
sentence: **before your first
top-level `COMMIT`, everything runs as one transaction; after it, each
top-level statement runs on its own, exactly as in psql.**

More exactly, the transition happens at the first top-level transaction
terminator — `COMMIT`, `END`, `ROLLBACK` or `ABORT` — and a deploy.sql
containing none of them stays entirely atomic. The scaffolded templates use the
other shape: they open with `BEGIN`, commit after the test gate, and carry their
closing banner in the psql-mode tail.

The mechanism follows directly from the implicit-block problem above. Everything
up to and including that terminator is sent to the server as one message and is
therefore one transaction; every statement after it is sent as its own message
on the same connection. That is what makes a concurrent index legal there — not
a flag, but the fact that it genuinely arrives alone.

![One PostgreSQL session split by the first top-level COMMIT: the atomic head sends ALTER TABLE, ADD CONSTRAINT NOT VALID and the test gate as one message and one transaction; after the boundary each tail statement is sent on its own, so CREATE INDEX CONCURRENTLY is legal, an explicit BEGIN/COMMIT forms a real transaction, and the session temp views stay readable on both sides](/pgmi/docs/diagrams/a05-execution-boundary.drawio.svg)

That sentence is the entire feature. Here is the four-phase change written
against it — this is [`examples/lock-safe-deploy`](https://github.com/vvka-141/pgmi/tree/main/examples/lock-safe-deploy)
in the repository, reduced to its skeleton:

```sql
BEGIN;                                    -- phase 1: atomic
SET LOCAL lock_timeout = '250ms';         -- bound the WAIT for ACCESS EXCLUSIVE

DO $$                                     -- ADD COLUMN, ADD CHECK ... NOT VALID
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT p.path, p.content
        FROM pg_temp.pgmi_plan_view p
        JOIN pg_temp.pgmi_source_view s ON s.path = p.path
        WHERE s.is_sql_file AND p.path LIKE './migrations/%'
        ORDER BY p.execution_order
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

SAVEPOINT _tests;
CALL pgmi_test();                         -- invariants must hold before anything commits
ROLLBACK TO SAVEPOINT _tests;

COMMIT;                                   -- and only here are the locks released

SET lock_timeout = '3s';                  -- phase 2: psql mode from here

SELECT pg_temp.reap_invalid_index('idx_orders_customer', 'orders');  -- see below
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_customer ON orders (customer_id);

BEGIN;                                    -- phase 3: a phase that must be atomic says so
UPDATE orders SET status = 'archived'
WHERE status = 'pending' AND created_at < now() - interval '10 years';
COMMIT;

ALTER TABLE orders VALIDATE CONSTRAINT orders_amount_nonneg;   -- phase 4

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_status ON orders (status);
```

You can read the lock profile of the deployment top to bottom. The
strong-locking statements are in the first transaction; the scan-heavy ones are
below the `COMMIT`, taking weak locks, in autocommit where PostgreSQL will
accept them. With one exception, discussed below: cleaning up after a *failed*
concurrent build needs `ACCESS EXCLUSIVE` again, and the tail has to say so.

And now the hold-time rule earns its keep, because this shape has a cost. Phase
1 holds `ACCESS EXCLUSIVE` from the first `ALTER` until the `COMMIT` — which
means it holds it *through the tests*. Gating a schema change on tests that run
before the commit is the point of the pattern; the price is that the gate sits
inside the lock window.

Measure that window rather than guessing it, and be careful which number you
take. Instrumenting the transaction itself — `clock_timestamp()` at the top of
the head, again immediately before `COMMIT` — gives 19–20 ms on a steady-state
run of this project. The CLI reports about 400 ms for the same run. The
difference is connecting, scanning files and preprocessing, none of which holds
a lock on your table. Reading the deployment's wall-clock time as the lock
window overstates it by twenty-fold here, which is the same category of mistake
this section is about.

Twenty milliseconds is comfortable. It would not stay that way if the gate grew
into an integration suite, and the two honest resolutions are to keep the
pre-commit gate to fast invariants, or to commit earlier and accept that tests
no longer protect the change atomically. What the shape gives you is sight of
the choice — and a place to measure it.

Running it:

```
$ pgmi deploy . -d lock_safe_demo --force
Database "lock_safe_demo" does not exist; creating
Preparing session: scanning files, loading parameters
Loaded 4 files
Loaded 1 parameters
Executing deploy.sql
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_schema.sql
[pgmi] Test suite completed (2 steps)
[phase 2] plan survived COMMIT: 3 source file(s), 3 planned step(s)
✓ lock_safe_demo: 4 files loaded, 1 test macro(s) expanded in 1.38s
```

Both indexes end up `VALID` and the constraint ends up validated.

The `SET LOCAL` in phase 1 is not decoration. Running that same deployment
against the same database while one ordinary long-running read is open, with
and without it:

```
without SET LOCAL lock_timeout:   exit=0    10518 ms
with    SET LOCAL lock_timeout:   exit=13    1231 ms
```

The wrong one looks better. The deploy *without* a timeout **succeeded** — and
sat in the lock queue for ten seconds with an `ACCESS EXCLUSIVE` request
pending, every reader arriving in that window queuing behind it. Exit code 0,
and an outage. The one *with* the timeout failed in about a second, bounding its own
contribution to the queue; a reader issued immediately afterwards returned in
16 ms.

This is where the convergent tail pays for itself. Because *this* tail was
written to converge on a re-run — every statement in it idempotent by
construction — failing fast is affordable. That property is the author's, not
the tool's, and it is what turns "run the deployment again" into an adequate
retry loop without a flag on the CLI.

That `[phase 2]` line is doing quiet work too: it queries the session's own
temp views *after* the transaction that created them committed. PostgreSQL temp
tables default to `ON COMMIT PRESERVE ROWS`, belonging to the session rather
than the transaction — which is why the phases can share one program.

One corollary to be honest about: statements between `COMMIT`s are **not**
implicitly grouped. Phase 3 is a transaction because it says so; otherwise the
`UPDATE` would stand alone. Explicit boundaries are the feature, and writing
them is the price.

## What a failure leaves behind

The tail gives up atomicity — necessarily, given the rule above. You can still
compensate, since dropping an index is an ordinary statement, but you cannot
make the sequence all-or-nothing. The question is what you are left holding.

Breaking the last statement of the deployment above:

```
✗ lock_safe_broken: failed after 1.10s (unit 11 of 11 failed; 10 earlier unit(s) already committed)
pgmi: error: execution failed: ERROR: column "nonexistent_column" does not exist (SQLSTATE 42703)
```

Ten units committed; the eleventh did not. The first index exists and is valid,
the backfill ran, the constraint is validated, the second index was never
created. That state is not a bug — it is the expected state of an autocommitted
sequence: PostgreSQL cannot make a sequence containing a concurrent index build
atomic. Tools differ in how clearly they report, resume, or repair what is left. What matters is that it is *reported*, down to which unit failed
and how many had already committed, and that the file is written so a corrected
re-run converges rather than colliding:

```
$ pgmi deploy . -d lock_safe_broken --force
Preparing session: scanning files, loading parameters
Loaded 4 files
Loaded 1 parameters
Executing deploy.sql
relation "orders" already exists, skipping
column "status" of relation "orders" already exists, skipping
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_schema.sql
[pgmi] Test suite completed (2 steps)
[phase 2] plan survived COMMIT: 3 source file(s), 3 planned step(s)
relation "idx_orders_customer" already exists, skipping
✓ lock_safe_broken: 4 files loaded, 1 test macro(s) expanded in 0.39s
```

That convergence is not free either. For a corrected whole-file re-run to
converge, every tail statement has to survive being executed again — and the
concurrent indexes are where that gets interesting, because `IF NOT EXISTS` is
not enough. A failed build leaves an `INVALID` index behind, and `IF NOT EXISTS`
matches it by name and skips over it forever. The index never gets rebuilt and
the deployment reports success every time.

Reaping that leftover is the one place the tail needs a strong lock, and it is
worth being blunt about: an ordinary `DROP INDEX` takes `ACCESS EXCLUSIVE` on
the table. Nothing about being in the autocommit phase softens that. So the
cleanup carries the same short bound as phase 1 rather than the tail's more
relaxed one, and it takes the lock explicitly before deciding. This runs inside
`pg_temp.reap_invalid_index()`, a helper the head defines and the tail invokes
through a top-level `SELECT`. The function does not own a transaction — no
function does; it runs in its caller's. What owns one here is that `SELECT`,
which in psql mode is its own single-statement transaction. That is what gives
`SET LOCAL` something to be local *to*: it holds across the lock and the
re-check, then resets when the statement commits. Written as a bare
autocommitted statement of its own, it would have no useful effect at all:

```sql
SET LOCAL lock_timeout = '250ms';
EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE', p_table);

-- Re-check while holding it. An index being built by a CONCURRENTLY in another
-- session is also indisvalid, and may have become valid since the probe above.
IF EXISTS (SELECT 1 FROM pg_index WHERE indexrelid = v_index AND NOT indisvalid) THEN
    EXECUTE format('DROP INDEX %s', v_index::regclass);
END IF;
```

Both halves matter. The timeout keeps the recovery path from recreating the
queue this series is about — on a busy table the deploy now fails in 642 ms with
`55P03` instead of parking an `ACCESS EXCLUSIVE` request, and a reader issued
straight afterwards returned in 11.8 ms. The re-check under the lock closes a
race that the probe alone cannot: `indisvalid = false` does not distinguish
*your* failed leftover from *someone else's build in progress*, and dropping
the latter destroys healthy work.

## Honest limits

**pgmi does not classify DDL safety.** It will run a table-rewriting `ALTER`
inside your one transaction, at three in the afternoon, against your largest
table, and report success. Every safe decision above is the engineer's. If you
want a second pair of eyes, [Squawk](https://squawkhq.com/) and
[Eugene](https://kaveland.no/eugene/) catch unsafe patterns in review and are
worth having in CI — though neither can see your production table size, your
bloat, or the report someone is running right now. Use them *and* control your
transaction boundaries; the tool contributes boundaries you can see, not
judgement about what belongs on either side of them.

**Tools that do more, do more.** [pgroll](https://github.com/xataio/pgroll)
implements versioned schemas as views, bidirectional dual-write triggers, and
batched backfill from a single declarative file — strictly more automation than
what is described here, at the cost of an app-side `search_path` coordination
step during the migration window. [reshape](https://github.com/fabianlindfors/reshape)
takes a similar views-and-triggers approach. If you want the database to manage
the expand/contract dance for you, these are the tools that do it.

**This is the database half only.** A phased deploy.sql cannot make old and new
application code coexist. pgroll and reshape do carry part of that weight in
the database, through views and dual-write triggers; pgmi carries none of it.
Either way, application rollout, version routing and cut-over coordination
remain app-tier concerns.

**The backfill in phase 3 is a demonstration, not a template.** It is atomic
because it is small. A large `UPDATE` holds row locks for its duration, writes
proportional WAL, leaves dead tuples behind, and can put replicas behind — none
of which batching removes, though batching does shorten the lock window and let
autovacuum keep up. Backfills at scale need batching and progress tracking, and
that is a different article.

**pgmi's model needs a session.** pgBouncer's feature matrix marks
`PRESERVE`/`DELETE ROWS` temporary tables, `SET`/`RESET`, and session-level
advisory locks as unsupported under transaction pooling — which is precisely
the set pgmi relies on. Deploy through a direct connection or a session-pooled
one.

## The checklist

For any schema change, in any tool:

1. What lock does each statement take, and does it block reads or only writes?
2. Does any statement hold `ACCESS EXCLUSIVE` across a table scan?
3. Is `lock_timeout` set on the session doing the work, and is it smaller than
   any *non-zero* `statement_timeout`?
4. How long is the *transaction* that holds the strong lock — what else runs
   between the `ALTER` and the `COMMIT`?
5. Which statements cannot run in a transaction, and where is that boundary
   written down?
6. If the deployment fails after that boundary, what is left applied — and does
   re-running converge?

The first four arise from PostgreSQL's behaviour and you should be able to
answer them whatever you deploy with. The fifth asks how your deployment
represents its answer. The sixth decides what that representation costs you at
three in the morning.

A short `lock_timeout` and a weaker-locking form genuinely reduce risk; that
part is PostgreSQL's, and it works whatever you deploy with. What a deployment
program adds is somewhere for the rest of it to live: the waits bounded, the
commit points visible, the partial states designed before production discovers
them. Not risk removed by decree — risk you can read, and therefore risk you
can choose.

The phased deployment, the mid-tail failure and the convergent recovery are all
in
[`examples/lock-safe-deploy`](https://github.com/vvka-141/pgmi/tree/main/examples/lock-safe-deploy),
asserted by branch, pull-request and release CI. The lock-queue reproduction and the timing
comparisons in this article were run by hand against PostgreSQL 16 and are not
part of that suite — the three sessions and the `pg_sleep` are a few lines of
psql, and reproducing them yourself is the fastest way to stop believing that
fast means safe. The full execution contract is in the
[deploy.sql guide](https://vvka-141.github.io/pgmi/docs/deploy-guide/). For how
the migration files inside phase 1 get their order — and why that order is a
query result rather than a filename convention — see
[Your Migration Numbers Are a Distributed Counter Without Coordination](https://vvka-141.github.io/pgmi/articles/migration-numbers-distributed-counter/).

## Sources

- PostgreSQL: [Explicit Locking](https://www.postgresql.org/docs/16/explicit-locking.html) · [CREATE INDEX](https://www.postgresql.org/docs/16/sql-createindex.html) · [CREATE TABLE (ON COMMIT)](https://www.postgresql.org/docs/16/sql-createtable.html)
- Flyway: [executeInTransaction](https://documentation.red-gate.com/flyway/reference/configuration/flyway-namespace/flyway-execute-in-transaction-setting) · Liquibase: [runInTransaction](https://docs.liquibase.com/concepts/changelogs/attributes/run-in-transaction.html) · goose: [annotations](https://pressly.github.io/goose/documentation/annotations/) · golang-migrate: [PostgreSQL driver README](https://github.com/golang-migrate/migrate/blob/master/database/postgres/README.md)
- [pgroll](https://github.com/xataio/pgroll) · [reshape](https://github.com/fabianlindfors/reshape)
- [pgBouncer feature matrix](https://www.pgbouncer.org/features.html)
