---
title: "Scenario-Tree Testing in PostgreSQL: Every Authored Branch, Shared History, Before COMMIT"
date: 2026-08-25
author: "Alexey Evlampiev"
description: "Express the branching scenarios of your business logic as a directory tree; walk it with savepoints so each branch inherits its history instead of rebuilding it; and let the walk decide whether your deployment commits."
weight: 2
---
# Scenario-Tree Testing in PostgreSQL: Every Authored Branch, Shared History, Before COMMIT

*Express the branching scenarios of your business logic as a directory tree; walk it with
savepoints so each branch inherits its history instead of rebuilding it; and let the walk
decide whether your deployment commits.*

*By Alexey Evlampiev*

**Abstract.** Database tests often repeat the same state-building work, because several
scenarios share the same prefix: a device may be provisioned before testing its
configuration paths; an order may be paid before testing shipment and refund; a workflow
may be approved before testing its downstream outcomes. The running example throughout is
an order lifecycle — chosen only because its branching states are easy to see, and standing
in for whatever lifecycle your own database implements. To test both `placed → paid → shipped` and
`placed → paid → refunded`, a conventional suite constructs `placed → paid` twice. This
article develops the alternative: express the scenarios as a directory tree and walk it
with PostgreSQL savepoints — execute the shared prefix once, test one branch, roll back to
the branch point, and test its sibling from the same inherited state. Every scenario then
runs against the accumulated state it actually depends on, without rebuilding that state
and without seeing a sibling's changes. A lifecycle's reachable histories branch like a
multiverse, far beyond what a practical suite can cover, so the tree is authored, not
exhaustive: you choose the critical paths, and the walk *proves* each one from the exact
parent state it depends on — proof here meaning execution plus declared-invariant checks,
not formal verification. And because most PostgreSQL DDL is transactional, the whole walk can run inside a
still-uncommitted deployment: apply the migration, run the tree, discard the test state,
and commit only if every authored scenario passes.

The argument runs in five parts: the scenario-tree pattern itself; what bounded, authored
proof buys over exhaustive histories; the walk as the deployment's commit gate; a live
run — a green walk, a failing branch, a synthetic stress run; and the costs and limits. Two
appendices carry the lineage and the PostgreSQL subtransaction internals.

But the destination fits in one screen, so here it is before the argument — the sections
below build every piece this sketch uses:

```text
__test__/                  -- the scenario tree: every directory is a business state
  _setup.sql               -- root state: a product in stock
  order_placed/            -- its _setup.sql places the order
    test_placed.sql        -- tests assert against the state they sit in
    cancelled/             -- branch: cancel it; assert the stock is restored
    paid/                  -- sibling branch: pay it — never sees the cancellation
      shipped/
        delivered/         -- deepest scenario: placed → paid → shipped → delivered
      refunded/
```

The runner walks the tree depth-first, with a savepoint per directory plus one the tests
roll back to. After a branch finishes, `ROLLBACK TO` the savepoint taken before that
branch's transition restores the shared parent state. Run that walk inside the deployment
transaction, and the tree decides whether the migration commits.

## The pattern

The pattern applies to PostgreSQL-backed state machines whose relevant transitions can run
inside one transactional session; an order lifecycle makes the mechanics easy to see.

An order is placed. From that moment the lifecycle branches: the customer pays, or
cancels. If they paid, the order ships, or the payment is refunded. Each branch has its own
invariants — conditions that must hold in that state: after cancellation the reserved stock
must be back on the shelf; refunds must
never total more than the captured payment; a retried payment message must post once, not
twice. Testing `pay()`, `cancel()`, and `refund()` individually is not enough. Correctness
also depends on **which state they run from and which transitions came before them**.

Database tests are commonly organized as straight lines: build state, walk one path, assert,
discard. Scenarios that share a long history — `placed → paid → shipped` and
`placed → paid → refunded` — each rebuild `placed → paid` unless the harness preserves it
transactionally. That repeated prefix is the *shared history*; as it grows, setup cost grows
with it, making deep scenarios progressively more expensive to test and pressuring suites
toward shallower ones.

Savepoints are the way out. Note what the savepoint
names mean: a savepoint marks the state *before* a transition — the branch point you return
to — not the state you are about to test.

```sql
BEGIN;

INSERT INTO product (sku, stock) VALUES ('WIDGET', 5);
SELECT place_order('WIDGET', 2);          -- shared history: the order exists

SAVEPOINT before_cancel;
SELECT cancel_order(...);                 -- transition into the cancelled state
-- assertions that hold after cancellation
ROLLBACK TO SAVEPOINT before_cancel;      -- back to the branch point

SAVEPOINT before_pay;
SELECT pay_order(...);                    -- the sibling transition
-- assertions that hold after payment
ROLLBACK TO SAVEPOINT before_pay;

ROLLBACK;   -- or COMMIT — the deployment gate below turns on this choice
```

The placement ran once. Both branches inherited it. Neither saw the other's transactional
changes.

The toy SQL shows only the rollback semantics. `ROLLBACK TO` leaves the named savepoint in
place; a recursive runner also releases each savepoint as it leaves the branch — which is
why live savepoint nesting follows tree depth rather than the number of siblings already
visited.

Maintaining that bookkeeping by hand would obscure the scenario model; a runner can derive
it from something engineers already know how to read — a directory tree:

```text
__test__/
  _setup.sql               -- root state: a product in stock
  order_placed/
    _setup.sql             -- transition: place the order
    test_placed.sql
    cancelled/
      _setup.sql           -- transition: cancel it
      test_cancelled.sql
    paid/
      _setup.sql           -- transition: pay it
      test_paid.sql
      refunded/
        ...
      shipped/
        ...
        delivered/
          ...
```

**The directory structure is the scenario tree.** Each directory represents a state. The
root `_setup.sql` establishes the initial state; each non-root `_setup.sql` applies the
transition into its directory's state from the inherited parent state. Descendants inherit
the result. Before the next sibling runs, the current branch is rolled
back to its branch point.

Shared setup usually raises a legitimate concern: test contamination. The distinction here
is that siblings never share their *changes* — only their established history. Both
`cancelled/` and `paid/` inherit the placed order because both require it; the rollback
makes each sibling's own transactional changes disappear before the next starts. **Sibling tests are
independent; their setup is not.**

That is the entire idea. Three terms carry the rest of the article. A **state** is the
database condition relevant to the lifecycle at one point in its history. A **transition**
is the business action that moves the lifecycle into its next state; the root `_setup.sql`
establishes the initial state, and each non-root `_setup.sql` applies the transition into
its directory's state. A **scenario** is one path
through those states. When scenarios share part of that path, they form the scenario tree.
And throughout this article, to *prove* a scenario means to execute that path and check its
declared invariants — not exhaustive or formal verification.

The walk visits each authored branch, checks its invariants, and unwinds it before the
sibling begins. In a standalone test run, an outer rollback erases the test transaction; in
the deployment gate below, an outer test savepoint removes the test state while preserving
the migration for commit.

![The depth-first savepoint walk inside one uncommitted deployment transaction: migrations run first, then seven states from root to delivered are visited in order, a savepoint taken before each transition; after each branch a ROLLBACK TO restores the branch point so the next sibling inherits the same history; if every authored branch passes, ROLLBACK TO _tests removes the test state and the schema commits — any violated invariant aborts the whole transaction](/pgmi/docs/diagrams/a07-scenario-tree-walk.drawio.svg)

Three boundaries before going further:

- **Assertions are separate.** The tree manages state; inside each node, assert however you
  prefer — `DO` blocks, pgTAP functions, anything that raises on failure.
- **The boundary is one transactional session.** Concurrency, commit-time behavior, and
  external side effects need separate tests; the limits section returns to each.
- **The tree is executable documentation.** Its lifecycle structure is readable by engineers
  straight off the directory listing and machine-checkable by tooling — checkable for shape
  (the root establishes its initial state, every non-root directory has its transition,
  assertions attach to states), not for whether a
  directory's name tells the truth about what its transition does; choosing the branches
  that matter and the invariants that define correctness remains a domain decision.

## From exhaustive histories to bounded authored proof

The pattern above is the entire mechanism. What remains is what it buys — because the case
for walking a tree begins with what proving a transactional system would even mean.

### What proving a transactional system means

A database application is a program with memory. That changes what correctness means, and
testing theory saw it early. A passage that has stood across editions of *The Art of
Software Testing* singles out
exactly this case: in a database application, "the execution of a transaction … is dependent
upon what happened in previous transactions. Hence, not only would you have to try all
unique valid and invalid transactions, but also all possible sequences of transactions."
That obligation has the shape of the scenario tree: correctness depends not only on
individual transactions but on the histories that lead into them. Production state
accumulates the way the passage describes — earlier transactions establish state that later
transactions read and modify — so reaching any state of interest means the chain of
transactions that built it has already run. And at many reached states more than one
transition is possible — the placed order is paid or cancelled — each outcome opening
further branches. Correctness over the lifecycle depends on that whole branching space:
every transition must behave correctly *from the accumulated state it actually runs against*.

Proving each operation once, from a fresh database, does not establish path correctness; it
establishes the operation in isolation — and the bug may live between operations.
"Cancellation restores the stock the placement reserved" is not a property of
`cancel_order`; it is a property of the path *placed → cancelled*.
**History-dependent invariants live on paths, not in individual operations** — and coverage
inside each operation does not establish coverage of the sequences between them. The gap is not peculiar
to database code: NIST's
[structured-testing guide](https://nvlpubs.nist.gov/nistpubs/Legacy/SP/nistspecialpublication500-235.pdf)
illustrates it with two consecutive `IF` statements — two tests reach 100% statement and
branch coverage while the bug in the statements' *interaction* survives untested, a compact
analogue of two consecutive lifecycle transitions, each exercised while their interaction
goes unvisited.

The exhaustive ideal for lifecycle-history coverage is to exercise every reachable
history. Real systems add
retries, loops, and unbounded data, so that space often needs a bound before it is even
finite; once bounded, exhaustive exploration still becomes infeasible quickly. Myers priced
the paths of a single twenty-iteration loop at
about 10¹⁴ — a billion years at one five-minute test each. Model checking faces the
related combinatorial wall known as the *state-explosion problem*.

Every practical technique that explores such a space has to bound it somewhere. Bounded model checking bounds depth:
exploration stops after a chosen number of transitions. Combinatorial testing bounds
*interaction strength* — rather than covering every combination or ordering, it covers
interactions only up to a chosen size, justified by the empirical finding that observed
failures involve few interacting factors. NIST's sequence variant is blunt about state: "in
many cases, a particular state must be reached before a particular failure can be
triggered"; eighty events admit 80! orderings, and forty-two tests cover every three-event
ordering.

An authored scenario tree imposes a third kind of bound: authorship. The people who know
the lifecycle choose the branches that matter — not all of them, the critical ones. The walk
then proves each from the exact parent state it depends on. That is the precise claim this
method makes — bounded and authored — and the limits section returns to what it
deliberately does not claim.

### Different transaction bugs need different tests

Database testing covers several failure classes, and they need different techniques.
Scenario trees target **history-dependent bugs**: failures that appear only after a
particular sequence of prior transitions has established the state in which the next
transition runs. They do not replace concurrency testing, which needs
multiple sessions, nor tests of commit-time behavior. The stakes are real, and the failures
do not announce themselves: one study verified 22 exploitable transaction-logic defects
across twelve widely deployed eCommerce platforms, while another — studying transaction
bugs in database engines rather than application logic — found that they overwhelmingly
fail *silently*, leaving plausible but wrong state with no crash. Appendix A carries
both studies. A strategy that exercises histories must therefore assert the resulting
state, not merely observe that each transaction completed.

### Why suites walk straight lines instead

The practical obstacle is usually not writing the assertion — it is reconstructing enough
history to reach the state worth asserting. Fernando Borretti's
[Composable SQL](https://borretti.me/article/composable-sql) (2025)
describes the experience — populating the foreign-key graph recursively until "testing
the simplest query requires building up a massive object graph, just to test an infinitesimal
slice of it." That object graph is the accumulated state, reconstructed by hand for every test.

Mainstream frameworks provide transactional test isolation, but the unit is the individual
test: Django's `TestCase` and pgTAP's `runtests()` add one shared transaction around
per-test blocks, and no default expresses shared history recursively across nested sibling
scenarios. Appendix A surveys the frameworks and the long record of requests to go deeper.

Copy-on-write *database branching* attacks the same setup-cost problem at a different
layer: it shares
committed state across sessions, where savepoints share uncommitted state within one. A Neon or Lakebase database
branch appears in constant time regardless of database size and genuinely carries the
committed history with it — but it reproduces *committed* state as a separate database
branch, and it cannot fork the uncommitted transaction on the
connection already running your scenarios, where a savepoint is a transaction-local marker.
The two compose rather than compete: use a database branch for the environment and the
committed prefix — one per pull request, one per test run — and walk savepoints over the
scenario tree inside it. What stays unreusable without savepoints is precisely the shared
history of sibling branches inside one transaction — the *common prefix* this article is
about.

## Gate the deployment before it commits

Once the tree can run inside one transaction, PostgreSQL offers a second advantage: the
transaction can contain the deployment itself. Because most DDL is transactional, the entire
scenario tree can run *inside the deployment transaction*:
the tree tests the schema and procedures your migration just created, and those schema changes
stay uncommitted until every scenario passes.

That test gate — assertions inside the deployment transaction, with `COMMIT` conditional on
them — is the subject of an earlier article,
[Test PostgreSQL migrations before COMMIT](https://vvka-141.github.io/pgmi/articles/test-postgresql-migrations-before-commit/).
What changes here is what is being gated: a scenario tree rather than a flat list of
assertions. The tree is then not a stage in a pipeline that runs near the deploy — it is
the deploy's own commit condition.

Migration → scenario tests → commit. Operationally: start the deployment transaction, apply
the migration, run the scenario tree, roll the test data back, and commit only if every
scenario passed. One contract makes the gate real: a violated invariant must propagate out
of the tree — or be re-raised after collecting failures — so that it aborts the outer
transaction. Two silent-pass paths break that contract: an `EXCEPTION WHEN OTHERS` handler
anywhere in the tree can swallow the failure and let the deploy commit green, and an empty
tree — discovery broke, or a filter matched nothing — looks exactly like a passing suite,
so treat zero discovered tests as a hard failure. With the contract intact, candidate
schema and test state disappear together: the deployment is rejected before its schema
could ever commit.

One boundary to state immediately. This is not "run your whole suite inside production
deployments": migration locks — many `ALTER TABLE` forms take `ACCESS EXCLUSIVE` — are held
until commit, so the tree's runtime adds to the lock window. Deployment-time trees should be small
and scoped to what the deploy touches; big trees belong in CI or against a disposable
database branch.

The composition depends on transactional DDL and on savepoint recovery from ordinary
statement errors — PostgreSQL guarantees both; the limits section spells out why.

Against a real target, the walk extends the deployment transaction and with it that lock
window. Bound lock waits with transaction-local timeouts, keep the deployment on a
connection that preserves session state, and give the statements PostgreSQL refuses inside
transaction blocks their own idempotent phase after the commit — the limits section
carries the full operational treatment.

Finally, persist the pass/fail record outside the transaction. PostgreSQL messages sent to
the client are not rolled back with database state, so the deployment runner can capture
them in its durable log even when the deployment transaction aborts — the one escape from
the transaction that works in your favor.

## Running it

Nothing here requires pgmi: any runner that walks the tree depth-first and brackets each
branch with `SAVEPOINT` / `ROLLBACK TO` implements the pattern. A runner needs four
conventions: which file establishes a directory's state (`_setup.sql` here — initial state
at the root, transition elsewhere), which files are assertions
(the directory's other `.sql` files), the order within a directory (transition, then
assertions, then children), and the sibling order (name order here).
[pgmi](https://github.com/vvka-141/pgmi), a deployment tool I build, is the implementation
used below. pgmi calls the `_setup.sql` files *fixtures*: in the scenario model, the root
fixture establishes the initial state and each non-root fixture applies the transition into
its directory's state. pgmi derives the depth-first walk from the `__test__/` tree shown
above. The shape is deliberate — a tree a
deployment can walk, letting the database evolve in every authored direction and proving
each one before committing. The deployment integration is about twenty lines.
`pgmi_test()` expands into the depth-first savepoint walk; the outer transaction runs the
migrations, executes the scenarios, and commits only if they pass:

```sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    )
    LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

SAVEPOINT _tests;
CALL pgmi_test();      -- expands into the depth-first savepoint walk
ROLLBACK TO SAVEPOINT _tests;

COMMIT;
```

The loop orders migrations by path for brevity; a project that declares sort keys reads
`pgmi_plan_view` instead, where
[execution order is a query result](https://vvka-141.github.io/pgmi/articles/migration-numbers-distributed-counter/)
rather than a filename convention.

Deployed against PostgreSQL 17 in a local container — timings illustrative, transcript
abridged in the middle:

```text
[pgmi] Fixture: ./__test__/_setup.sql
[pgmi] Test: ./__test__/test_root.sql
[pgmi] Fixture: ./__test__/order_placed/_setup.sql
[pgmi] Test: ./__test__/order_placed/test_placed.sql
[pgmi] Fixture: ./__test__/order_placed/cancelled/_setup.sql
[pgmi] Test: ./__test__/order_placed/cancelled/test_cancelled.sql
[pgmi] Fixture: ./__test__/order_placed/paid/_setup.sql
[pgmi] Test: ./__test__/order_placed/paid/test_paid.sql
...
[pgmi] Test suite completed (24 steps)
committed state: 0 product rows, 0 order rows
✓ demo2: 19 files loaded, 1 test macro(s) expanded in 0.34s
```

Seven states across five levels of nesting, twenty-four steps, 0.34 seconds — and after
`COMMIT`, the deployed schema remains while the test-created product and order rows are
gone. Branch-level rollbacks removed each branch's changes as the walk returned, and the
final `ROLLBACK TO SAVEPOINT _tests` removed the remaining transactional test data before
the outer commit. The deployment committed only after every authored branch had passed
against the candidate schema, and the test-created rows stayed invisible to other sessions
throughout. The transaction itself is still observable — its locks can block other
sessions — and some effects survive the rollback: sequence advances are not undone, and
anything that leaves the transaction can be observed outside it.

The failing run demonstrates what the deployment gate changes. Introduce a defect into one
branch's implementation — make `cancel_order` forget to restore the reserved stock — and
deploy to a fresh database:

```text
[pgmi] Fixture: ./__test__/order_placed/cancelled/_setup.sql
[pgmi] Test: ./__test__/order_placed/cancelled/test_cancelled.sql
✗ broken2: failed after 0.53s
pgmi: error: execution failed: ERROR: Failed in ./__test__/order_placed/cancelled/test_cancelled.sql: cancel must restore stock to 5, got 3 (SQLSTATE P0001)
```

Exit code 13. The database afterward contains no tables at all. The migration had executed; the walk reached
the scenario whose invariant the implementation violated; the exception aborted the
transaction; schema and test states vanished together. When deployment is gated this way, a
migration that violates a tested invariant never becomes the database's schema — for
everything inside the gated transaction; a separate concurrent phase stays outside the
gate.

The branch savepoints exist to explore successful alternative histories, not to swallow
failed assertions: the runner issues `ROLLBACK TO` only after a step succeeds, so an
unhandled exception propagates out of the whole script and aborts the transaction.

### Make the tree tell the truth

Three conventions make trees like this hold up in practice.

**Prefer real transitions over hand-built state.** Below the root, a `_setup.sql` should
normally move the inherited parent state into this directory's state by calling the same
transactional operations the application calls — direct row manipulation is the exception,
not the default. (Reference data inverts this: a catalog with no lifecycle is rightly
[declared as desired state](https://vvka-141.github.io/pgmi/articles/desired-state-reference-data-postgresql/)
rather than reached through transitions — here the lifecycle *is* the thing under test.)
That discipline is what makes the directory tree a truthful map: fabricated rows
can create a state the application could never reach through its real transitions, and a
test against such a state proves the operation, not the lifecycle path the tree claims to
represent.

**Expose the identities transitions create**, so tests and child transitions never have to
rediscover or guess them:

```sql
-- order_placed/_setup.sql
CREATE TEMP TABLE world_order AS
SELECT place_order('WIDGET', 2) AS id;
```

**Assert relative to the inherited test state.** This demo can assert against
database-wide counts because it deploys into an empty database. A real target already
contains unrelated state: the root fixture should introduce isolated test entities rather
than assume an empty database, descendant transitions should mutate those entities through
the normal application operations, and assertions must be scoped to those entities rather
than to global counts.

### How the demo checks sibling isolation

One test in the demo is a **runner test, not an application-testing pattern**: it
deliberately exploits the demo's deterministic sibling order — `cancelled/` executes first,
so `paid/` can detect any leaked state. Application assertions should not depend on sibling
order.

```sql
-- paid/test_paid.sql (abridged)
DO $$
DECLARE
    v_status text;
BEGIN
    -- cancelled/ sorts before paid/, so that sibling already ran and was
    -- rolled back. If its rollback leaked, this fails.
    SELECT o.status INTO STRICT v_status
    FROM sales_order o
    JOIN world_order w ON w.id = o.id;
    IF v_status <> 'paid' THEN
        RAISE EXCEPTION 'expected status paid, got %', v_status;
    END IF;
END $$;
```

**Demo note.** `sales_order` avoids the reserved word `ORDER`; the tests use
`RAISE EXCEPTION` rather than `ASSERT` because PostgreSQL can disable PL/pgSQL assertions
with `plpgsql.check_asserts = off`.

## Costs and limits

A depth-first walk keeps live savepoint nesting proportional to tree depth, not total tree
width: once a branch finishes, it rolls back before its sibling begins. Total
subtransaction work still grows with the number of writing branches. In a
synthetic stress run, a generated tree of 121 states — depth five, fanout three, every node
asserting its complete ancestor lineage and the absence of any non-ancestor state — ran its
363 steps in 0.75 seconds on the same container. Restated as straight lines — every
scenario rebuilding its path from the root — the same 121 scenarios execute 547 transitions
where the walk executes 121, at the same step count. The wall clock follows the transition
count: with transitions that only record an event, the straight lines land within noise of
the walk (0.88 s against 0.75 s, medians of five runs); with each transition also writing a
thousand rows of working data, they take 2.9 s against the walk's 1.2 s. What sharing the
prefix saves is proportional to what a transition costs. Wide write-heavy trees still consume
transaction IDs and can create WAL and replica-side costs; Appendix B explains the
PostgreSQL internals, including the 64-subtransaction cache threshold.

The boundaries:

- **One session, one timeline.** A scenario tree cannot test interleavings between sessions —
  concurrency needs concurrent harnesses. (Serializable isolation narrows what concurrency
  can break, but deadlocks, lock contention, and retry behavior still need concurrent tests —
  a gap that stays empty by default: one production codebase I reviewed carried a
  hundred-plus SQL test files and
  [zero concurrency tests](https://vvka-141.github.io/pgmi/articles/ai-agents-write-postgresql-like-python/).
  Run the walk itself at READ COMMITTED unless isolation behavior is itself under test;
  Appendix B explains the savepoint and predicate-lock interaction.)
- **One transaction, one boundary.** The tree runs each path inside one transaction by
  construction, so it cannot tell you whether production draws its transaction boundaries in
  the same places — an operation split across two production transactions still passes when
  the tree wraps it in one. Wrong-boundary defects are the largest class ACIDRain found, and
  they need their own review. (The gap closes by construction when the boundary is a
  declaration the operation itself carries and the database enforces — the argument of
  [The Request Becomes a Transaction](https://vvka-141.github.io/pgmi/articles/request-becomes-a-transaction/).)
- **One transaction, one clock.** `now()` and `transaction_timestamp()` return the same
  instant at every node of the walk, so a rule like "the refund window closes thirty days
  after payment" cannot be exercised by letting real time pass between nodes. Inject the
  business date as a parameter each transition reads, rather than calling `now()`.
- **Commit-time behavior needs its own phase.** `NOTIFY` delivery and post-commit hooks never
  fire inside the tree. Deferred constraints are partly testable inside it —
  `SET CONSTRAINTS ALL IMMEDIATE` forces the check at a point you choose, with the violation
  surfacing at a savepoint you can roll back — but statements that refuse transaction blocks
  (`CREATE INDEX CONCURRENTLY`, `VACUUM`, `CREATE DATABASE`) are unavailable here too.
- **Not everything rolls back.** Sequence advances survive by design — one more reason a
  walk keys its rows by UUID rather than by a sequence value; session-level advisory
  locks survive; anything that leaves the transaction — `COPY TO PROGRAM`, `dblink`, log
  output — escapes entirely. And rollback is logical, not physical: rolled-back test rows
  were still written to the heap and the WAL, and their traces can reach replicas and
  physical backups until vacuum and backup retention age them out — use synthetic fixture
  data, never copies of real records. A transition whose real implementation *is* an external effect — issuing a
  certificate, calling a broker — needs decomposing into a transactional core the tree can
  exercise and an effect dispatch it cannot.
- **The tree is a tree.** An acyclic lifecycle maps onto directories cleanly; a state
  reachable from several parents, or a cycle back into an earlier state, must be authored
  once per path that reaches it — the walk reuses history up to each divergence, but it
  cannot merge branches back together.
- **The walk is serial.** Suite parallelism is unavailable inside one tree; the payback comes
  from reusing shared history, not from parallel workers. Parallelism returns one level up —
  trees in separate database branches or databases run concurrently. Re-running one failing
  leaf means
  re-walking its ancestor path, and a broken transition fails its whole subtree.
- **Bounded, authored testing — not model checking.** The claim is never "all scenarios were
  tested"; it is "every authored branch was exercised from the parent state it depends on,
  without rebuilding that parent history." Savepoints change the economics of the scenarios
  you author; they do not discover the ones you missed.

### Running the gate against a real target

The gate's lock window is a budget: use `SET LOCAL lock_timeout` inside the gated
transaction to bound how long each statement waits to acquire its locks — `SET LOCAL`,
because a session-level setting leaks into the later concurrent-index phase, where a tight
bound aborts `CREATE INDEX CONCURRENTLY` and leaves an invalid index behind. A waiting
`ACCESS EXCLUSIVE` request also
[turns every later conflicting request into a blocked one](https://vvka-141.github.io/pgmi/articles/lock-queue-fast-is-not-safe/),
the long transaction pins the vacuum horizon while it runs, and on PostgreSQL 17+
`transaction_timeout` can cap the whole gated transaction.

The session-scoped state — temp tables, settings — must outlive individual transactions, so
connect directly, or through a session-level pooler, rather than a transaction-mode one.
Statements PostgreSQL refuses inside transaction blocks — `CREATE INDEX CONCURRENTLY` among
them — cannot participate in the gated transaction and belong in the deployment's
[psql-mode tail](https://vvka-141.github.io/pgmi/articles/transaction-boundary-in-the-program/);
they remain outside this proof, and each tail statement autocommits on its own, so a
mid-tail failure leaves earlier ones applied — write the tail so a corrected re-run
converges. (pgmi's
[lock-safe-deploy example](https://github.com/vvka-141/pgmi/tree/main/examples/lock-safe-deploy)
collects the full operational treatment: per-phase timeouts, invalid-index recovery,
idempotent tails.)

### Why PostgreSQL

The composition leans on two PostgreSQL guarantees. Most DDL is transactional, so a
migration can stay uncommitted while the tree runs against it. And ordinary statement
errors are recoverable through savepoints: `ROLLBACK TO` is documented as
"the only way to
regain control of a transaction block that was put in aborted state by the system due to an
error" — the walk can always return to a branch point with the outer deployment transaction
alive. Serialization failures are the caveat, and `SERIALIZABLE` is not the line: already
at REPEATABLE READ a concurrent update can fail the walk with an error whose documented
remedy is retrying the whole transaction, and under SERIALIZABLE a serialization failure
dooms the whole transaction, savepoints included. Run the walk at READ COMMITTED unless
isolation behavior is itself under test.

Other engines realize the pattern only up to a point. The fail-fast walk shown above ports
to SQL Server directly — a failing branch aborts the whole deployment either way. What does
not transfer is recovering a failed branch and continuing: SQL Server documents an
uncommittable transaction — reached under `SET XACT_ABORT ON` or a sufficiently severe
error — in which "the session can't commit the
transaction or roll back to a savepoint". Oracle's implicit DDL commits rule out the
deployment gate entirely, though utPLSQL demonstrates the savepoint-isolated test walk
itself. Savepoints are standard SQL; the guarantees this composition needs are not.

## Takeaway

A transactional database evolves through branching histories, and scenario tests should
preserve that shape: let the state grow into every authored future, prove each from the
exact history it depends on, unwind each branch to its shared prefix, and erase the
remaining test state before commit. Savepoints make that walk affordable — a test returns
to a shared branch point instead of rebuilding the path to it — and PostgreSQL's
transactional DDL lets the walk gate the deployment that ships it.

That gate is a placement decision as much as a testing one: the evidence and the commit
it authorises hold in one transaction, which is what makes the result a guarantee rather
than a report. [A Decision Belongs Where Its Authority Lives](https://alexeyevlampiev.github.io/locality-of-authority/)
argues the general case.

Start small. Pick one lifecycle your database already implements — an order, a device, a
job, an approval, a subscription, a claim — with three or four meaningful states. Do not redesign your testing stack. Represent just that lifecycle as
directories, put each transition in a `_setup.sql`, and see how much repeated
shared-prefix setup disappears. (If you start from `pgmi init`, the harness is already
wired: the scaffolded deploy.sql gates its commit on `CALL pgmi_test()`, and the
`__test__/` directory follows this convention — the nesting is yours to add.) After that,
adding a branch means describing only what changed: one directory
holding its transition and its assertions — never another retelling of what came before.

Author the branches. Inherit the history. Commit only what every branch survives.

---

## Appendix A: evidence and lineage

The underlying ideas are old; the uncommon part is combining them into the
directory-derived scenario tree and deployment gate used here.

Testing theory has known since Tsun Chow's
[1978 paper](https://archiv.infsec.ethz.ch/intranet_secured/r/1/chow-testingFSMs.pdf) on
finite-state machines that a test suite for a stateful system is naturally a tree — his method
builds a "testing tree," and he notes that "many sequences have common prefixes and thus could
be verified in groups." What the classical work lacked was a cheap way *back* to a shared
prefix; its only return path was a full reset.

Databases have had that cheap return for as long as they have had savepoints. Jim Gray's
[1978 notes](https://jimgray.azurewebsites.net/papers/dbos.pdf) describe the savepoint as "a
firewall that allows a transaction to stop short of total backup" and already use the word
*backtrack*; Eliot Moss's [1981 nested-transaction
model](https://publications.csail.mit.edu/lcs/pubs/pdf/MIT-LCS-TR-260.pdf) made the branching
explicit — on a child's failure, "a parent might retry a failed child, or try to accomplish
the same end in another way."

Depth-first exploration over savepoints has direct precedent. A
[TACAS 2013 model checker](https://users.ece.utexas.edu/~gligoric/papers/GligoricMajumdar13DPF.pdf)
restored database state on backtrack via savepoints, noting the approach "works only for
depth-first search exploration because there can be at most one sequence of savepoints in a
database"; Microsoft holds a patent on stacked-savepoint test hierarchies (US 9,501,386,
filed 2014 — after the TACAS work above was published — granted 2016);
[utPLSQL v3](https://www.utplsql.org/utPLSQL/latest/userguide/annotations.html) ships nested
savepoint-isolated test contexts for Oracle.

The web-application strand of the lineage is Evan Miller's 2010 essay
[Functional Tests as a Tree of Continuations](https://www.evanmiller.org/functional-tests-as-a-tree-of-continuations.html).
It describes the problem exactly — when sibling paths keep replaying an ever-longer shared
prefix, total setup work grows quadratically with path length — and the remedy:
"automatically make a copy of the universe at the end of Step 1, then run child tests on each
parallel universe." The image fits the mechanism exactly: the tree is a multiverse of the
lifecycle — sibling branches are alternative futures of one shared history, and along each
scenario the database evolves from an early beginning toward one target state, the tree
holding every authored direction of that evolution in one structure. A commenter on the
essay's Hacker News thread named savepoints as the mechanism
the same week, blocked only by their ORM. When the essay resurfaced on Hacker News in 2025, the
thread held people still wishing it existed. David Wheeler had asked the larger question at
[PGCon 2009](https://www.pgcon.org/2009/schedule/events/165.en.html), introducing pgTAP: "why
is it that we don't write database unit tests?" Sixteen years of naming the gap, from that
question to that thread. The
branching idea was already present; what remained uncommon was packaging it into everyday
database-test tooling.

The framework record says the one-level ceiling is felt. Django's `TestCase` wraps the
class in one transaction with a nested per-test block; pgTAP's `runtests()` runs each test
as a subtransaction inside one transaction; Rails wraps tests in rollback transactions by
default, and Spring's transactional test support similarly rolls back each participating
test. Testcontainers' own guidance recommends sharing one database container across the
suite, and its Spring Boot guide resets application data with `deleteAll()` before each
test; PostgreSQL 18's `file_copy_method = CLONE` lets template copies share disk blocks on
capable filesystems — ever-cheaper environments, the same straight-line state strategy. The
tickets asking to nest deeper,
like pgTAP's [#96](https://github.com/theory/pgtap/issues/96), have been open for years. A
Ruby gem, [`rspec_nested_transactions`](https://github.com/rosenfeld/rspec_nested_transactions),
has wrapped nested RSpec contexts in savepoints since 2013; beyond that, I could not find a
commonly used PostgreSQL framework that turns nested scenario structure into a savepoint tree.

The cost side has its own evidence. Gerard Meszaros's *xUnit Test Patterns* put tests against
a real database at roughly fifty times the cost of in-memory tests, and the bill shows up at
scale: [Shopify](https://shopify.engineering/spark-joy-by-running-fewer-tests) found that even after
deleting tests, compute fell only about a quarter, "because a significant chunk of computing time is
still used for setting up containers, databases, and pulling caches"; an early Discourse
attempt to share pre-built state was
[abandoned](https://github.com/discourse/discourse/pull/7414) because "a significant number of
the tests assume they are starting with a blank slate."

The stakes side has its own studies. The [ACIDRain
study](https://www.bailis.org/papers/acidrain-sigmod2017.pdf) (SIGMOD 2017) verified 22
exploitable defects across twelve widely deployed eCommerce platforms — transaction
boundaries drawn wrong, so an invariant holds within each statement but not across the
whole operation — and 17 of the 22 survive even SERIALIZABLE isolation: the fix has to be
in the logic, and the logic needs tests. ACIDRain's exploits are themselves
concurrency-driven, which is why concurrency keeps its own harness. A
[2024 study](https://gaoyu-cn.github.io/paper/2024-icse-txbug.pdf) of 140 confirmed
transaction bugs in six database *engines* themselves found that 76.4% fail *silently* —
wrong data, wrong query results, no crash; almost all needed no more than three
concurrently interleaved transactions over tables of a few rows, and 94.3% triggered
deterministically under a fixed multi-session schedule. Transaction
bugs can leave plausible but wrong state without producing a crash — the failure mode
behind the main text's rule that history-exercising tests must assert the resulting state.

One story from a different bug class — concurrency — is worth keeping for what it says
about method. When
[Jepsen tested PostgreSQL 12.3](https://jepsen.io/analyses/postgresql-12.3) in 2020, a
generative checker surfaced in a two-minute run a serializability defect that nine years of
hand-picked example transactions had passed over. The moral is about method: systematic
exploration can find failures that hand-picked examples miss. A scenario tree does not
provide that generation; it provides a structured execution model for a bounded, authored
subset of the histories a single session can reach.

What I could not find, after specifically searching for earlier implementations, is an
existing tool combining two properties this article leans on: the scenario tree derived from the directory structure
itself, and the walk running inside a still-uncommitted deployment. If one exists, I want to
hear about it.

## Appendix B: the subtransaction internals

If you only care about using the pattern, you do not need this appendix; it explains why a
depth-first walk avoids accumulating live subtransactions across sibling branches, and which
subtransaction costs remain.

### Why depth-first traversal bounds live subtransactions

The 64-subtransaction cache is not a 64-node or 64-sibling tree limit: what matters is how
many non-aborted subtransaction XIDs are live at the same time.
PostgreSQL caches up to 64 subtransaction XIDs per backend (`PGPROC_MAX_CACHED_SUBXIDS` in
`proc.h`, PostgreSQL 17); past that, snapshots are marked *suboverflowed* and visibility
checks fall back to the `pg_subtrans` SLRU — the cliff behind the "subtransactions
considered harmful" literature, which benchmarks savepoints held open (Laurenz Albe measured
a 60% throughput drop moving from sixty to ninety open writing savepoints). The cache counts
**non-aborted** subtransactions: `RecordTransactionAbort` removes an aborted child's XIDs
immediately, which is why the number of live cached subxids in a walk whose branches all end
in `ROLLBACK TO` is bounded primarily by depth. pgmi's emitted walk also releases each
directory's savepoint as it leaves the branch, so nothing accumulates across siblings;
measured on PostgreSQL 17, two hundred rolled-back writing subtransactions left the cache at
zero, unoverflowed.

### Costs rollback does not erase

Three residuals. Every writing subtransaction consumes a real XID whether or not it rolls
back (500 rolled-back subtransactions advance the counter by 501 — vacuum-freeze pressure at
scale). Once a transaction does exceed 64 live subxids the overflow flag is sticky for its
remaining lifetime (cleared only at transaction end — `ProcArrayEndTransactionInternal`).
And whenever `wal_level` is `replica` or higher — the default — PostgreSQL emits a WAL
assignment record for every 64 *assigned* subxids, and rollback does not retract it; with a
hot standby attached, wide trees inside long transactions can therefore degrade replica
snapshots — the shape behind
GitLab's replica incident, whose transactions never nested more than ten deep.

### Locks and SERIALIZABLE

A lock acquired after a savepoint "is released immediately if the savepoint is rolled back
to" (documented in §13.3); on subtransaction *commit* the source (`resowner.c`) transfers
them to the parent — Moss's 1981 rule, abort discards, commit inherits. Predicate locks are
the deliberate exception: under SERIALIZABLE, SSI keys all predicate locking to
the top-level transaction precisely so that "predicate locks must survive a subtransaction
rollback" (README-SSI) — `ROLLBACK TO` does not undo the reads. A walk cannot conflict with
itself, but its SIREAD footprint grows monotonically, and once the predicate lock table runs short of memory
and page-level predicate locks coarsen into relation-level ones, the documentation warns of
rising serialization-failure rates —
one more reason to run the walk at READ COMMITTED unless serializable behavior is the thing
under test.

All transcripts and measurements in this article were produced on PostgreSQL 17.

---

*Alexey Evlampiev builds data platforms on PostgreSQL.
[pgmi](https://vvka-141.github.io/pgmi/) is MPL-2.0 open source.*
