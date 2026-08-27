---
title: "Your ALTER TABLE Is Fast. The Queue Behind It Is Not."
date: 2026-08-01
author: "Alexey Evlampiev"
description: "How a PostgreSQL lock queue turns a six-millisecond schema change into a sixteen-second outage, and the two moves that bound the damage."
weight: 4
---

# Your ALTER TABLE Is Fast. The Queue Behind It Is Not.

*A schema change that runs in six milliseconds can still stop every read on the table for sixteen seconds — the damage is done by waiting, not by working.*

*By Alexey Evlampiev*

Here is the same statement, run twice against the same table — PostgreSQL 16.14
in a local container, an `orders` table of 100,000 rows, the contending session
held open with `pg_sleep`. These are illustrative numbers from one machine, not
a benchmark; the ratio is the point, and you can reproduce it in about a minute.

```
-- Uncontended:
ALTER TABLE orders ADD COLUMN note text;
Time: 5.786 ms

-- With one ordinary long-running SELECT open on the table:
ALTER TABLE orders ADD COLUMN note text;
Time: 18065.050 ms (00:18.065)
```

The second run is not slow because the work is harder. The work is identical
and takes about six milliseconds either way — adding a column with no default
touches no rows at all, it only edits the catalog. The eighteen seconds are
spent *waiting for a lock*, and while that statement waits, it does something
worse than being slow: it stops unrelated readers that would otherwise have run
instantly.

That is the part worth internalising, and it holds no matter which migration
tool you use. Uncontended duration tells you what a statement costs when nothing is in its
way; it does not predict what it costs your users. Lock mode, time spent
waiting, and the length of the surrounding transaction complete that picture —
and only the first number shows up in your migration timings.

Lock-safe deployment has two halves. One is statement design: choosing SQL
forms that take weaker locks, or defer their expensive work to a point where
the lock is weak. The other is program structure: choosing where your
transaction boundaries fall, because a lock lives until its transaction ends
rather than until its statement does. This article is about the first half. It
is pure PostgreSQL and it applies whatever you deploy with.

## The queue is the mechanism

Three sessions, in this order. The first is an ordinary long read — a report,
an analytics query, an idle transaction someone left open:

```sql
BEGIN;
SELECT count(*) FROM orders;   -- holds ACCESS SHARE for the whole transaction
```

The second is the migration, wanting `ACCESS EXCLUSIVE`, which conflicts with
everything including plain reads. It waits. The third session arrives *after*
the migration and asks for nothing more than a plain `SELECT`.

The third session should be fine. `ACCESS SHARE` does not conflict with
`ACCESS SHARE`; the only granted lock on the table is the first session's, and
the two are compatible. Here is what actually happens:

```
 pid | state  |  wait   |                    query                     | blocked_by
-----+--------+---------+----------------------------------------------+------------
 101 | active | Timeout | BEGIN; SELECT count(*) FROM orders; SELECT p | {}
 108 | active | Lock    | ALTER TABLE orders ADD COLUMN note text;     | {101}
 115 | active | Lock    | SELECT count(*) FROM orders;                 | {108}
```

Read the last column. Session 115 is not blocked by the long read it is
compatible with. It is blocked by **the migration** — by a statement that does
not yet hold the `ACCESS EXCLUSIVE` lock it asked for. The lock table says the
same thing:

```
 pid |        mode         | granted
-----+---------------------+---------
 101 | AccessShareLock     | t
 108 | AccessExclusiveLock | f
 115 | AccessShareLock     | f
```

![Three sessions on one table: session 1 holds ACCESS SHARE for the whole transaction; session 2's ALTER waits 18.1 s for ACCESS EXCLUSIVE, blocked by session 1; session 3's ordinary SELECT waits 16.0 s, blocked by the ALTER rather than by the read it is compatible with](/pgmi/docs/diagrams/a04-lock-queue.drawio.svg)

A waiting lock request is not passive. Conflicting requests queue behind it
rather than jumping ahead — which is what stops a stream of short readers from
starving a writer forever, and the price of that fairness is this failure mode:
one blocked `ACCESS EXCLUSIVE` request turns every later read into a blocked
read, until the original transaction ends. The innocent `SELECT` above took
16,018 ms. Nothing was wrong with it; it waited for a schema change that was
itself waiting for a report.

This consequence is not stated in those words in the PostgreSQL manual — the
manual documents the conflict matrix, and the queueing behaviour follows from
it. The clearest write-ups of the practical failure come from
[Xata](https://xata.io/blog/migrations-and-exclusive-locks) and from
Postgres.ai, whose
[lock-tree queries](https://postgres.ai/blog/20211018-postgresql-lock-trees)
exist precisely because you cannot diagnose this from the blocked statement
alone — you have to walk the chain.

## Fast is not safe

Once you see that waiting is the hazard, the operational question changes. Not
"how long does this migration take?" but "what lock does it take, and how long
might it have to wait for it?"

The short version of the taxonomy, for PostgreSQL 16 and up:

`ALTER TABLE` takes `ACCESS EXCLUSIVE` unless a subform is documented
otherwise, so the interesting question is what each subform *does* while
holding it:

| Operation | Lock | Statement duration | What still works |
|---|---|---|---|
| `ADD COLUMN`, non-volatile default | ACCESS EXCLUSIVE | catalog only | nothing |
| `ADD COLUMN`, volatile default | ACCESS EXCLUSIVE | full rewrite | nothing |
| `SET NOT NULL` | ACCESS EXCLUSIVE | full scan, unless a validated `CHECK` proves non-null | nothing |
| `ADD CHECK ... NOT VALID` | ACCESS EXCLUSIVE | catalog only | nothing |
| `VALIDATE CONSTRAINT` | SHARE UPDATE EXCLUSIVE | full scan | reads and writes |
| `ADD FOREIGN KEY` | SHARE ROW EXCLUSIVE (both tables) | validates existing rows | reads |
| `CREATE INDEX CONCURRENTLY` | SHARE UPDATE EXCLUSIVE | two passes, slower | reads and writes |

`SET NOT NULL` and a validating `ADD CHECK` scan the whole table under
`ACCESS EXCLUSIVE` — the canonical "instant on my laptop, outage in
production", because the scan is proportional to a table your development
database does not have.

`SET NOT NULL` has an escape, and it is worth knowing: since PostgreSQL 12 it
can skip the scan when a *validated* `CHECK` constraint already proves the
column contains no nulls. That turns the dangerous statement into a four-step
recipe where only the cheap steps take the strong lock:

```sql
ALTER TABLE orders ADD CONSTRAINT orders_ref_notnull CHECK (customer_ref IS NOT NULL) NOT VALID;
ALTER TABLE orders VALIDATE CONSTRAINT orders_ref_notnull;   -- SHARE UPDATE EXCLUSIVE
ALTER TABLE orders ALTER COLUMN customer_ref SET NOT NULL;   -- no scan; the CHECK proved it
ALTER TABLE orders DROP CONSTRAINT orders_ref_notnull;       -- now redundant
```

One is often mis-remembered. `ADD FOREIGN KEY` has taken `SHARE ROW EXCLUSIVE`
rather than `ACCESS EXCLUSIVE` since PostgreSQL 9.5, so plain reads keep
working — but writes to *both* tables still wait, and the statement still
validates the existing rows. GoCardless's much-cited 2016 incident, roughly
fifteen seconds of API downtime from adding a foreign key, has an obsolete lock
profile and a lesson that has not aged at all; the library they wrote in
response still ships short lock and statement timeouts by default.

And a non-volatile default has not rewritten the table since PostgreSQL 11:
`now()` is `STABLE`, so a `now()` default is fine; `gen_random_uuid()` and
`clock_timestamp()` are the ones that rewrite.

None of this is new knowledge, and none of it is mine. The canonical catalogue
is [strong_migrations](https://github.com/ankane/strong_migrations), which has
encoded this taxonomy as a Ruby-side check for years and prints a numbered safe
recipe when you trip one. If you work in Rails, use it. What follows is the
same knowledge, expressed as SQL you run rather than a gem that inspects you.

### Three quantities, not one

That table has a column for how long each statement *runs*. That is not how
long its lock is *held*. PostgreSQL keeps table-level locks until the end of
the transaction that took them, not until the statement finishes — so "catalog
only" describes six milliseconds of work and says nothing about the next ten
minutes. Three quantities decide whether a deployment hurts:

- **Acquisition time** — how long the statement waits for its locks.
  `lock_timeout` bounds each individual acquisition attempt, so a statement
  taking several locks can exceed it in total; `statement_timeout` bounds the
  statement as a whole.
- **Statement duration** — how long the work runs once it has its locks. Weaker
  forms do not shorten this. `CREATE INDEX CONCURRENTLY` takes *longer* than
  `CREATE INDEX`; what it reduces is what it blocks while running.
  `statement_timeout` is what bounds duration.
- **Hold time** — how long the acquired locks stay held: until the transaction
  ends, by whatever route, `COMMIT` or `ROLLBACK` or the session going away.
  PostgreSQL 17 added [`transaction_timeout`](https://www.postgresql.org/docs/17/runtime-config-client.html)
  to put a ceiling on that. Earlier releases have no general
  transaction-duration timeout at all.

Most safe-migration advice covers the first two and leaves the third implicit,
which is why a "fast" statement inside a long transaction still causes an
outage: an `ALTER TABLE` running in six milliseconds at the top of a five-minute
transaction blocks readers for five minutes. The statement was never the
problem; the transaction was. Hold time is also the quantity statement-level
advice cannot reach — it is decided by where the deployment puts its `COMMIT`,
not by which statement you chose.

## Two moves

**Move one: refuse to wait.** Set `lock_timeout` on the deploying session so
that a statement which cannot get its lock quickly gives up instead of holding
the queue hostage. The identical collision from the first section, with a
250 ms timeout on the migration:

```
SET lock_timeout = '250ms';
ALTER TABLE orders ADD COLUMN note3 text;
ERROR:  canceling statement due to lock timeout
Time: 262.083 ms
```

The ordering matters, so to be exact: the long read was still open and had
about eleven seconds left to run. The migration gave up at 262 ms. A reader
issued after that — with the long transaction still holding its `ACCESS SHARE`
lock — ran immediately:

```
SELECT count(*) FROM orders;
Time: 6.299 ms
```

Sixteen seconds became six milliseconds, and note *why*. `lock_timeout` does
not let queued readers overtake a waiting `ACCESS EXCLUSIVE` request; nothing
does. It removes the request from the queue, after which reads are once again
only competing with a lock they never conflicted with. The migration failed,
which is the point — it failed cheaply, bounding its own contribution to the
queue at about 250 ms rather than the eleven seconds it would otherwise have
spent there, and it can be retried once the long transaction is gone. A retry loop around that is the standard
completion of the pattern, and Postgres.ai's write-up on
[lock_timeout and retries](https://postgres.ai/blog/20210923-zero-downtime-postgres-schema-migrations-lock-timeout-and-retries)
makes the case for exponential backoff and jitter rather than a fixed interval.
GitLab's `with_lock_retries` is the best-known implementation, applying a short
timeout across many attempts rather than one long block. GoCardless ship a
750 ms `lock_timeout` and a 1500 ms `statement_timeout` by default, on the same
fail-fast reasoning.

One caveat that bites people: `lock_timeout` must be *smaller* than a non-zero
`statement_timeout` to have any effect, and a unitless value is milliseconds.
`deadlock_timeout` is a different setting that only schedules the deadlock
check; it never aborts anything on its own.

**Move two: split the change so the expensive parts take weak locks.** The
constraint recipe is the model. Instead of one statement that scans the table
under `ACCESS EXCLUSIVE`:

```sql
ALTER TABLE orders ADD CONSTRAINT orders_amount_nonneg CHECK (amount >= 0) NOT VALID;
-- ... later, separately:
ALTER TABLE orders VALIDATE CONSTRAINT orders_amount_nonneg;
```

The first statement takes the strong lock only long enough to edit the catalog.
The second does the scan under `SHARE UPDATE EXCLUSIVE`, while reads and writes
keep flowing. Same end state; the unavailable window is the brief one.

Indexes work the same way: `CREATE INDEX CONCURRENTLY` blocks neither reads nor
writes, at the cost of a slower build and one hard constraint — **it cannot run
inside a transaction block.** That is a server-side PostgreSQL rule that no tool
can opt out of, and there are two distinct ways to trip it, with two different
errors:

```
DO $$ BEGIN EXECUTE 'CREATE INDEX CONCURRENTLY idx_a ON orders (customer_id)'; END $$;
ERROR:  CREATE INDEX CONCURRENTLY cannot be executed from a function

SELECT 1; CREATE INDEX CONCURRENTLY idx_b ON orders (customer_id);
ERROR:  CREATE INDEX CONCURRENTLY cannot run inside a transaction block
```

The first is an *execution context* bar — it fires even if the block commits
first, so concurrent builds can never be driven from a loop the way ordinary
migration files can. The second is subtler and matters more than it looks: a
multi-statement query is itself an implicit transaction block, so even "put the
concurrent index after the `COMMIT`" fails if the whole script reaches the
server in one message.

So the statement that exists to avoid blocking your users cannot sit inside the
transaction that the rest of your deployment wants to be. Something has to give,
and every deployment tool has had to decide what.

## What to carry away

Five questions answer themselves once the three quantities are separate:

1. What lock does each statement take, and does it block reads or only writes?
2. Does any statement hold `ACCESS EXCLUSIVE` across a table scan?
3. Is `lock_timeout` set on the session doing the work, and is it smaller than
   any *non-zero* `statement_timeout`? (The default `statement_timeout` is 0,
   meaning no limit, and a zero value never overrides `lock_timeout`.)
4. How long is the *transaction* that holds the strong lock — what else runs
   between the `ALTER` and the `COMMIT`?
5. Which statements cannot run in a transaction at all?

The first three are about the statement, and good tooling helps with them:
[strong_migrations](https://github.com/ankane/strong_migrations) for the
taxonomy, [Squawk](https://squawkhq.com/) and
[Eugene](https://kaveland.no/eugene/) as linters in CI. None of them can see
your production table size or the report someone is running right now, but they
catch the patterns worth catching.

The last two are different in kind. They are not about which statement you
write; they are about where your `COMMIT` goes — and that is a property of the
deployment program, not of any single migration. Most migration frameworks
represent the answer as metadata attached to a migration unit. What changes
when the boundary becomes an ordinary SQL statement in the program instead is
the subject of the companion to this article: *Your Transaction Boundary Belongs in the
Program, Not the Filename*.

## Sources

- PostgreSQL 17: [`transaction_timeout`](https://www.postgresql.org/docs/17/runtime-config-client.html)
- PostgreSQL: [Explicit Locking](https://www.postgresql.org/docs/16/explicit-locking.html) · [ALTER TABLE](https://www.postgresql.org/docs/16/sql-altertable.html) · [CREATE INDEX](https://www.postgresql.org/docs/16/sql-createindex.html) · [lock_timeout](https://www.postgresql.org/docs/16/runtime-config-client.html)
- [strong_migrations](https://github.com/ankane/strong_migrations) · [activerecord-safer_migrations](https://github.com/gocardless/activerecord-safer_migrations)
- [GitLab: Migration Style Guide](https://docs.gitlab.com/development/migration_style_guide/)
- [Squawk](https://squawkhq.com/) · [Eugene](https://kaveland.no/eugene/)
- Postgres.ai: [lock trees](https://postgres.ai/blog/20211018-postgresql-lock-trees) · [lock_timeout and retries](https://postgres.ai/blog/20210923-zero-downtime-postgres-schema-migrations-lock-timeout-and-retries) · Xata: [migrations and exclusive locks](https://xata.io/blog/migrations-and-exclusive-locks)
