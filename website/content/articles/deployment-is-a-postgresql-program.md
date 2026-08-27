---
title: "Your Deployment Is a PostgreSQL Program"
date: 2026-08-26
author: "Alexey Evlampiev"
description: "Every migration tool is a program that executes your SQL. Invert the control flow and deployment policy becomes SQL the project owns."
weight: 1
---

# Your Deployment Is a PostgreSQL Program

*Every migration tool is a program that executes your SQL. Invert the control flow and deployment policy becomes SQL the project owns.*

*By Alexey Evlampiev*

**Abstract.** Every team that adopts a migration tool eventually goes looking for a flag.
The release needs one thing the tool did not anticipate — a check that must run after the
schema change but before the commit, a concurrent index built in the middle of an
otherwise transactional deployment, an ordering rule that filenames cannot express — and
the search begins: through the configuration reference, then the issue tracker, then the
changelog of a version that has not shipped. The flag is the visible symptom of an
invisible arrangement. A migration tool is a program that executes your SQL, which means
every deployment semantic — what runs, in what order, inside which transaction, and
whether the result is allowed to commit — belongs to the tool's vocabulary. Anything
outside that vocabulary is a feature request. This article describes the inversion: a tool
that prepares one PostgreSQL session, materializes the project as relations inside it, and
hands deployment policy to a SQL program the project owns. The tool keeps the execution
mechanism; the project takes the policy. What used to require a tool feature becomes SQL the
project owns.
The scope is the database semantics — choosing and ordering the work, controlling its
transaction boundaries, validating the result, and deciding whether it may commit — and not
the cloud APIs, secret stores, and approval gates around them. The costs are specific too, and
the last part names them.

![Two columns. On the left, an external migration tool: your migration files feed a dashed, undifferentiated box — the tool owns the deployment policy, meaning statement boundaries, transaction context, and the durable history of what ran — which then runs your migrations for you against your database. Policy lives in the tool's vocabulary. On the right, the same band is divided. Your project files feed a green box: pgmi keeps a narrow mechanism — session preparation, macro expansion, and the head/tail split. An amber arrow, hands over the policy, carries control to an amber box: your deploy.sql owns the policy — selection, ordering, transactions, and the commit gate — reaching the database through a COMMIT gated on its own tests. Policy is executable PostgreSQL code.](/pgmi/docs/diagrams/a08-mechanism-and-policy.drawio.svg)

The argument runs in five parts: what a migration tool outside the database is forced to
own; how deployment policy moves into PostgreSQL; what the handover actually consists of;
why the rest of the technique — ordering policy, test gates, lock-safe phasing — follows
from that one decision rather than sitting beside it; and what the inversion costs.

But the destination fits on one screen, so here it is before the argument — Part III defines
the deployment contract behind every object this sketch uses:

```sql
-- deploy.sql — the deployment, as a program
BEGIN;

DO $$
DECLARE
    v_file record;
BEGIN
    FOR v_file IN
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

CALL pgmi_test();   -- every test in its own savepoint
COMMIT;             -- reached only if every test passed
```

No external configuration decides that control flow, and no migration-tool flag produced
it. The loop defines the execution order because the project wrote the loop. The commit is
gated because the project placed the test call above it. These are ordinary SQL statements,
which means they are reviewed and changed as ordinary SQL.

## Part I — What an external migration tool has to own

A migration tool runs as a separate process. It reads files, opens a connection, and sends
statements. That position is not a design flaw, but it does force the tool to own three
responsibilities, and those responsibilities are what the tool's vocabulary has to encode.

**It has to recognize statement boundaries once its execution model needs them.**
PostgreSQL's [simple-query protocol](https://www.postgresql.org/docs/current/protocol-flow.html)
accepts a multi-statement string, so crossing a connection does not by itself require a
client-side split. The requirement appears one step later: a tool that executes statements
separately, classifies them, or varies their transaction handling has to know where each one
ends. Dollar-quoted function and `DO` bodies, semicolons inside string literals and comments,
and SQL routine bodies written with `BEGIN ATOMIC` make that a lexical problem, so any tool
that splits PostgreSQL scripts client-side implements enough of PostgreSQL's rules to tell a
statement terminator from syntax that only looks like one. That scanner becomes a second
compatibility surface between the SQL a project wrote and the SQL PostgreSQL eventually
sees.

**It has to decide the transaction context before the migration's own SQL runs.** A file
can of course contain `BEGIN` and `COMMIT` — the rest of this article depends on exactly
that. What the file cannot do is choose the context it is placed in, because the tool has
already opened or declined a transaction around it by the time the first statement runs.
When a statement cannot run inside a transaction block — `CREATE INDEX CONCURRENTLY` is the
standard case — that decision has to surface somewhere outside the statement itself. Flyway
exposes
[`executeInTransaction=false`](https://documentation.red-gate.com/fd/migration-transaction-handling-273973399.html)
for exactly this. The setting is sound; what it reveals is that the transaction boundary
has become metadata *about* your SQL rather than a statement *in* the deployment program.

**It has to maintain a model of what already ran.** The history table is a durable record
of what the tool believes it applied. Because that record is maintained separately from the
database state it describes, the two can diverge — and mature tools therefore need a
repair, baseline, or reconciliation path for the cases where they do.

None of this is an argument that these tools are badly built. They are, on the whole, very
well built, and for a linear sequence of `CREATE TABLE` and `ALTER TABLE` statements they
are the right choice — a point the last part returns to. The argument is narrower: the need
to own those responsibilities is structural. It follows from standing outside the database
and describing work that happens inside it. The 2026 restatement of that case,
[Why We Are Still Getting Database Deployments Wrong](https://alexeyevlampiev.github.io/posts/database-deployments-wrong-2026/),
works it through in operational detail; the relevant conclusion here is that no amount of
additional configuration removes the position that generates the need for configuration.

## Part II — Deployment policy moves into PostgreSQL

Change one thing: let the tool prepare a PostgreSQL session, put the project *inside* that
session as data, and then execute one file the project owns.

The three responsibilities do not vanish. What moves is the policy behind them — and it
moves in three different ways.

The tool no longer splits the migration files the deployment chooses to run. It hands the
project to `deploy.sql`, and when that program executes a file's `content` with `EXECUTE`,
PostgreSQL parses that SQL directly. The tool still recognizes a deliberately small amount
of structure in `deploy.sql` itself — the test macro, and the boundary between its atomic
head and its statement-by-statement tail. Part III makes both exceptions explicit,
because a design that claims a clean handover should say where the handover is not clean.

The tool no longer decides the transaction context. The program opens and closes its own
transaction with `BEGIN` and `COMMIT`, so the boundary is a statement in the
deployment rather than metadata attached to it — visible in review, and available to be
conditional. What pgmi still does with the statements *after* that boundary is a narrower
execution contract, described in Part III.

The tool no longer owns a durable model of what already ran. The deployment program can
maintain one, or not, in whatever shape the project needs — and query that model with SQL,
next to the data it describes, in the same transaction.

Statement splitting disappears from the migration-file path; transaction policy becomes
explicit control flow; deployment history, when retained, becomes ordinary relational state.
From there,
much of what used to be tool policy becomes something the project can query.

The general form has an analogue in inversion of control, though the useful version of it is
narrower than the aphorism. A migration tool owns the deployment loop and invokes each
migration according to its own execution model. Here the tool invokes one project-owned entry
point, and from there that program owns the loop and calls the session API. What inverts is
not which process starts first. It is who owns the deployment control flow — and the
interesting part is not the inversion itself but what the handover has to contain to make it
work.

## Part III — The handover is a small session API

The handover is the core of the design, and its public surface is small enough to state
completely. That session API exists before the project's `deploy.sql` runs and disappears
with the session. Two narrow pieces of preprocessing sit around it, and both are named
below.

### Files arrive as rows

The project's files are loaded into a session-scoped temporary table and exposed through a
view. Each row carries the path, the content as text, the directory, the extension, the
depth, the size, two checksums, and whether the extension is a recognized SQL one:

```sql
SELECT path, directory, is_sql_file, size_bytes
FROM pg_temp.pgmi_source_view
WHERE directory = './migrations/'
ORDER BY path;
```

Two consequences matter, because both enlarge what the deployment program can treat as
input.

*All* files are loaded, not only SQL. A project that carries a `project.json`, a CSV of
reference rows, or a YAML policy document gets those as rows too, and the deployment can
read them with `content::jsonb` in the same query it uses for migrations. The scaffolded
starter project does exactly that to read its own version number.

And each file carries two checksums, not one: a hash of the raw bytes, and a hash of
normalized content with comments stripped, case folded, and whitespace collapsed. One
answers "are these the same bytes?"; the other answers "did anything change that this
normalization treats as significant?" Which of those is the file's identity is a policy
question, so both are present and the program chooses.

### Parameters arrive twice

Values passed on the command line or in a parameters file are available as rows in
`pgmi_parameter_view`, and as session settings:

```sql
COALESCE(current_setting('pgmi.env', true), 'development')
```

The second form matters more than it looks. It means a parameter is readable from inside
any function the deployment calls, at any depth, without being threaded through as an
argument.

### The plan is a view, not a list

This is the object that makes the rest work. The execution plan is not a data structure
the tool holds and prints — it is a temp view, derived by joining the source table against
parsed metadata and unnesting each file's declared sort keys:

```sql
SELECT path, sort_key, execution_order
FROM pg_temp.pgmi_plan_view
ORDER BY execution_order;
```

Three properties follow from it being a view rather than a report.

It is **queryable**, so a project can assert on its own plan before executing it — a
manifest comparison, a rule that nothing may precede the tenancy migration, a check that
no two files claim one sort key. The assertion is an ordinary `EXCEPT` or `EXISTS` in the
same transaction as the deployment it governs.

It is **derived**, and one source file deliberately contributes several execution rows when
it declares several sort keys. An idempotent file can therefore run early to create the
roles and again later to grant on newly created objects, without being copied and kept in
sync by hand.

And its order does not depend on the server's locale. The ordering is taken under
`COLLATE "C"` —
byte order — because sort keys and path fallbacks mix in one column, and under a linguistic
collation the position of `.` relative to digits varies by locale. That removes server
locale as a source of plan drift: given the same project contents and the same contract
version, sort keys and path fallbacks order identically on a developer's laptop and in
production.

### Tests are a separate tree with its own contract

Files under `__test__/` are excluded from the source view and loaded into their own tables
and views, walked by a plan function that returns fixtures, tests, and teardowns in
depth-first order with a depth column. `CALL pgmi_test()` expands — before the SQL reaches
PostgreSQL — into inline SQL that runs that walk with savepoint isolation per test.

That expansion is the one place where the tool rewrites a project-level SQL construct.
There is one other, narrower inspection, and both are worth naming rather than hiding.

### Where the handover stops

The first stop is a rewrite. `CALL pgmi_test()` is a macro, expanded in Go, and its
expansion contains `SAVEPOINT`. PostgreSQL does not allow savepoints in the implicit
transaction block it creates for a multi-statement query, so the generated test SQL requires
an explicit `BEGIN ... COMMIT` block around it.

The second is a classification. pgmi locates the first top-level transaction terminator,
under a deliberately narrow lexical rule that excludes savepoint control,
prepared-transaction
commands, and the keyword closing a `BEGIN ATOMIC` routine body. Appendix A
gives the exact recognized forms.

pgmi sends the head through that terminator as one unit. The statements before the
terminator run in one transaction — the atomic head, where the test gate belongs — and the
terminator closes it. Everything after is sent statement by statement. Following an ordinary
terminator those statements run under
PostgreSQL's normal autocommit, matching psql's default execution model, which is what makes
`CREATE INDEX CONCURRENTLY` possible without a second tool invocation or a per-file setting;
a terminator carrying `AND CHAIN` deliberately leaves a new transaction open into the tail,
as Appendix A describes.

That split reads one token class, to preserve PostgreSQL's own rules rather than to add any
of the tool's. Its practical consequence is real: statements after a mid-file `COMMIT` are
not implicitly grouped, and a failure there leaves earlier autocommitted statements applied.
Tail work has to be safe to restart after partial success. A failed
[`CREATE INDEX CONCURRENTLY`](https://www.postgresql.org/docs/current/sql-createindex.html)
can leave an invalid index behind, so reissuing the same statement is not a recovery
strategy; the invalid index has to be dealt with explicitly before the deployment goes on.

### The public surface stays small

Six views, a handful of deploy-facing functions, and one composite type for test events form
the session API. The two pieces of preprocessing sit around it: the test macro and the
head/tail split. Together they are the public contract — and the tables and other machinery
behind it are free to change, while a deployment can pin the contract version it was written
against. A contract that small is one a team can hold in their head — and its smallness
is the point rather
than a limitation, because everything else is the project's own code.

## Part IV — Why the rest follows

The reason to argue this once, carefully, is that the techniques worth writing about
separately are not separate features. Each is a consequence of the handover, and reads
differently once the handover is visible.

**The test gate.** If the deployment program declares its own atomic head, then running
assertions after that work and before its commit is not a capability the tool grants —
it is a `CALL` placed above a `COMMIT`. The checks therefore run in the same
target-database transaction as the work being gated, against that database's actual
accumulated drift rather than a rehearsal copy. *Test PostgreSQL migrations before
COMMIT* develops it.

**The transaction boundary.** If the boundary is a statement, a phased deployment that
is transactional at the start and non-transactional at the end is expressible in one
file, rather than as metadata attached to two. *Your Transaction Boundary Belongs in the
Program, Not the Filename* develops it, and *Your ALTER TABLE Is Fast* covers why the
distinction is operationally urgent rather than aesthetic.

**Ordering and identity.** If the plan is a relation, then the question of what a
migration *is* — a filename, a number, a declared identity — becomes a question about
columns you can constrain, rather than a convention enforced by review. *Your Migration
Numbers Are a Distributed Counter Without Coordination* develops it.

**Desired-state data.** If a deployment is a program with the project's files as data,
then loading a catalog by diffing it against what exists is an ordinary query, not a
special mode. *From Seed Scripts to Desired-State Reference Data* develops it.

**Scenario testing.** If tests are a tree in the same session as the deployment, then
the tree can be walked with savepoints so branches inherit shared history, and the walk
can decide whether the deployment commits. *Scenario-Tree Testing in PostgreSQL*
develops it.

Read in the other direction, each of those articles is a technique that happens to use a
tool. Read from here, they are one decision, applied five times.



## Part V — What the inversion costs

The inversion moves work rather than removing it, and the work it moves is not trivial.

**You write the orchestration you used to inherit.** A migration tool's defaults represent
years of accumulated decisions about ordering, failure handling, and idempotency. Adopting
the inverted model means making those decisions yourself, in PL/pgSQL, and living with them.
A team without PL/pgSQL fluency should not choose this; the learning curve is the honest
first cost and there is no configuration that removes it.

**For the simple case this is worse.** If the deployment is a linear sequence of numbered
files applied in order, then the tool's model fits the problem directly, and the advantages
above do not repay the additional orchestration. Flyway is the better choice, with a
shallower curve and a larger ecosystem. This matters enough to be a decision rule: the
inverted model earns its cost when the deployment has *shape* — phases, conditions, gates,
data-dependent branching — and not before.

**A durable ledger is a real property, given up by default.** An external tool's history
table survives the session, the operator, and the tool itself. A model where the deployment
program decides what has run means an environment's history is exactly as good as the
program that maintains it, and a team that writes no tracking gets none. Three lines in the
scaffolded starter recover apply-once semantics, and the advanced template ships a full
tracking system — but the choice is now something a team makes, badly or well, rather than
something they inherit.

**One session is the model, and transaction pooling breaks it.** The handover lives in
session-local objects. A transaction-mode pooler does not hold a client to one backend
across transactions, so later statements cannot rely on the same `pg_temp` state being
reachable. Deployments need a direct connection or session pooling. This is not a
configuration detail to be worked around later — session continuity is the mechanism, and
where it is unavailable the approach is unavailable.

**Everything rides through one connection.** Because the project is materialized into one
session, this is not a streaming deployment architecture, and repository size is part of its
operating envelope. The documented envelope is hundreds of SQL files and dozens of data
files, not multi-gigabyte bulk loads. The limiting factors are insert throughput and wire
time, not memory. Bulk data belongs in `COPY`.

**The errors are PostgreSQL's errors.** No migration-specific error taxonomy is layered on
top. Failures surface as PostgreSQL SQLSTATEs and whatever context the deployment's own SQL
added; pgmi reports the outcome to its caller with a numbered exit code. Establishing the
connection retries transient failures, but executing your SQL does not. Nothing reinterprets a failure as a framework-level migration error, and nothing
tells you which migration to look at unless your program says so. For a DBA this is usually
preferable. For a team expecting a tool to interpret failures, it is a downgrade.

## Takeaway

A deployment tool that runs outside the database has to encode the decisions it cannot
delegate to the deployment itself — as defaults, conventions, metadata, or settings. Invert
the control flow — one session, the project materialized as relations, control handed to a
SQL program the project owns — and the policy behind those decisions moves into project-owned
SQL: the plan is a view you can assert on, the transaction boundary is a statement you can
read, and history, if you keep one, is a table you designed. The techniques that follow are
not features to be added but consequences to be noticed.

An external tool still provides the execution mechanism. What the inversion moves is
ownership of the policy.

The trade is stated plainly and does not improve with familiarity. You take on the
orchestration, the PL/pgSQL, the tracking decision, and the session-continuity requirement.
In exchange, nothing your deployment needs to do is a feature request.


## Appendix A — Exactly where pgmi splits `deploy.sql`

The head/tail split turns on the **first top-level transaction terminator**. The recognized
forms are `COMMIT`, `END`, `ROLLBACK`, and `ABORT`, in any of their `WORK`, `TRANSACTION`,
and `AND [NO] CHAIN` spellings.

The set is narrower than those keywords suggest. Three constructs read like terminators and
are not:

| Construct | Why it does not split the file |
|---|---|
| `ROLLBACK TO [SAVEPOINT] s` | Savepoint control, not transaction control |
| `COMMIT PREPARED` / `ROLLBACK PREPARED` | Acts on a previously prepared transaction, not the current one |
| `END` closing a `BEGIN ATOMIC` body | Closes an SQL routine body, not a transaction |

The last one is the subtle case. A `BEGIN ATOMIC ... END` routine body (PostgreSQL 14+, for
SQL functions and procedures alike) is not dollar-quoted, so its semicolons and its `END` are
visible to a naive scan; without the exclusion, the body's `END` would read as a `COMMIT`
synonym and split the file in the middle of a routine definition.

Terminators inside string literals, dollar-quoted bodies, and comments are masked before the
scan, so they do not split the file either.

Two consequences follow. A terminator carrying `AND CHAIN` starts a new transaction
immediately, so that transaction remains open into the statement-by-statement tail.

And a `deploy.sql` with no top-level terminator is sent as a single message. If that message
contains no explicit transaction control, PostgreSQL executes its statements as one implicit
transaction. An explicit `BEGIN` without a terminator instead leaves a regular transaction
open — which is worth knowing before writing a deployment that opens one and never closes
it.
