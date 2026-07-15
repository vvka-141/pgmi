---
title: "AI agents write PostgreSQL like Python"
date: 2026-07-15
author: "Alexey Evlampiev"
description: "Field notes from a production review of an AI-written PostgreSQL backend: exception blocks as control flow, casts that turn bad requests into 500s, races on state-advancing updates — and the four-phase handler discipline that contained them."
weight: 10
---

# AI agents write PostgreSQL like Python

*By Alexey Evlampiev*

Give a coding agent a PostgreSQL-first backend to build and it will write you working SQL. It will also, with remarkable consistency, write you *Python* — Python's exception-driven control flow, Python's trust in casts, Python's habit of validating wherever the code happens to be — transliterated into PL/pgSQL, where those idioms carry costs the agent never sees.

This article is a field report. It draws on two internal reviews of a production line-of-business backend built database-first — a few hundred SQL files across roughly two dozen domains, with a kernel/handler split and a thin HTTP gateway — where AI coding agents wrote most of the SQL. The reviews rated every finding and verified each against the code before reporting it. The failure modes below are the ones that recurred; the discipline in the second half is what the same codebase used, in its healthy majority, to avoid them.

One scoping note up front. Published work on LLM-generated SQL quality — Readyset's "Why LLMs write incorrect SQL," the Spider and BIRD text-to-SQL benchmarks — covers *declarative* failures: wrong joins, bad grouping, missing predicates. We found no published field reports on how agents fail at *procedural* SQL — PL/pgSQL exception handling, handler-layer control flow, boundary validation. That absence is why this report exists.

## Failure mode 1: `try/except`, in a language where catching means a savepoint

The single most consistent agent habit: signaling ordinary outcomes by raising exceptions, then catching them a layer up to pick an HTTP status. The reviews found a dozen or so kernel functions doing this — "not found," "already processed," "nothing left to assign" — all normal outcomes, all expressed as `RAISE EXCEPTION`.

The shape (anonymized, runnable):

```sql
-- Agent-written kernel: raises to say "nothing to do"
CREATE FUNCTION core.mark_alert_seen(p_id uuid)
RETURNS core.alert
LANGUAGE plpgsql
AS $$
DECLARE
    v_row core.alert;
BEGIN
    UPDATE core.alert
       SET is_seen = true, seen_at = now()
     WHERE id = p_id AND NOT is_seen
    RETURNING * INTO v_row;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'alert % not found or already seen', p_id
              USING ERRCODE = 'no_data_found';
    END IF;

    RETURN v_row;
END;
$$;

-- Agent-written handler: catches to pick a status code
BEGIN
    v_row := core.mark_alert_seen(v_id);
    RETURN api.json_response(200, to_jsonb(v_row));
EXCEPTION
    WHEN no_data_found THEN
        RETURN api.problem_response(404, 'Not Found');
END;
```

In Python, this is idiomatic. In PL/pgSQL, it is not a stylistic choice — it changes the transaction machinery. Every `BEGIN ... EXCEPTION` block establishes an implicit subtransaction: PostgreSQL sets a savepoint on entry so it can roll persistent changes back to the block boundary if a handler fires. The documentation is unambiguous about the cost: a block containing an `EXCEPTION` clause is "significantly more expensive to enter and exit than a block without one," and the manual's own tip is not to use `EXCEPTION` without need.

To be precise about the mechanics, because precision matters here: the subtransaction consumes a subtransaction XID only when the block performs DML — a read-only exception block is assigned none and is comparatively benign. And a single shallow exception block on a request path is cheap in absolute terms. The damage is a concurrency-and-scale effect, not a per-block tax.

PostgreSQL caches 64 subtransaction XIDs per backend; workloads that exceed that under load force snapshot visibility checks through the `pg_subtrans` SLRU, where they contend. GitLab's engineering team published the canonical incident: under a specific combination of a long-running transaction and subtransaction-heavy traffic, replica throughput collapsed from roughly 360,000 to 50,000 transactions per second, with unrelated queries timing out cluster-wide, until they eliminated subtransactions from hot paths — a workload-specific incident, cited here for the failure class rather than the numbers. An agent that stamps an exception block into every handler and kernel — once per request, on every request path — is quietly signing you up for that class.

The deeper problem is architectural, though, and it would remain even if subtransactions were free: the exception channel is being used as a *data* channel. The kernel knows a perfectly ordinary fact ("no unseen alert with that id") and encodes it as an error, which the handler must then decode by SQLSTATE. Two functions now share a contract that lives in neither's signature.

The fix deletes code. A kernel that returns `NULL` for "nothing to do" needs no exception machinery at all — and can usually stop being PL/pgSQL entirely:

```sql
-- Kernel: pure SQL. Returns the row, or NULL if there was nothing to do.
CREATE FUNCTION core.mark_alert_seen(p_id uuid)
RETURNS core.alert
LANGUAGE sql
AS $$
    UPDATE core.alert
       SET is_seen = true, seen_at = now()
     WHERE id = p_id AND NOT is_seen
    RETURNING *;
$$;

-- Handler: branches on the return value. No savepoint, no SQLSTATE contract.
v_row := core.mark_alert_seen(v_id);
IF v_row IS NULL THEN
    RETURN api.problem_response(404, 'Not Found', 'Alert not found');
END IF;
RETURN api.json_response(200, to_jsonb(v_row));
```

Same behavior, one implicit subtransaction fewer per request, and the outcome contract is now visible in the function's return type.

The reviews also caught the sibling pattern: kernels wrapping an `INSERT` in `BEGIN ... EXCEPTION WHEN check_violation` to re-raise a friendlier message — several more instances. The rule that replaces it: if a business rule can be a `CHECK` constraint, make it one, and validate the input *before* the statement instead of catching the wreckage after.

## Failure mode 2: raw casts turn bad requests into 500s

The second habit: trusting `::type` on user input. Agents write `(payload->>'assigneeId')::uuid` and `(query->>'limit')::int` the way Python developers write `int(request.args["limit"])` — except Python's `ValueError` was probably caught by the framework, and PostgreSQL's `invalid_text_representation` (SQLSTATE `22P02`) aborts the transaction and surfaces as an HTTP 500 for what is plainly the client's mistake.

The telling detail from the field: the reviewed codebase already had a safe-cast helper, used nearly everywhere. The raw-cast findings — a kernel iterating caller-supplied JSON with bare `::int`/`::uuid` casts, one handler with a raw path-param cast and raw pagination casts — were flagged by the reviewers as *the only call sites not using it*. Both were rated high severity: any malformed request body or URL produced a 500 instead of a 422 or 400. Agents didn't lack a correct pattern to imitate; they intermittently reverted to instinct.

What belongs at the boundary is a cast that cannot throw. Since PostgreSQL 16 the primitive is built in — `pg_input_is_valid()` and `pg_input_error_info()` validate a string against a type without raising and without a subtransaction:

```sql
SELECT pg_input_is_valid('not-a-uuid', 'uuid');
-- f

SELECT message, sql_error_code
  FROM pg_input_error_info('not-a-uuid', 'uuid');
--                      message                      | sql_error_code
-- --------------------------------------------------+----------------
--  invalid input syntax for type uuid: "not-a-uuid" | 22P02
```

That second function is worth pausing on: it hands you the exact message and SQLSTATE the cast *would* have raised, as data — precisely what a validation error response wants to carry. (Both functions rely on the type's input function supporting soft error reporting; effectively all common core types do in 16+, but extension types are not guaranteed to.)

On versions before 16 — and for types whose parsing you want to constrain more tightly than the input function does — the pattern is a `try_cast` function. The honest irony: a pre-16 `try_cast` for arbitrary types must use the very `EXCEPTION` block this article just criticized, paying the subtransaction cost inside the helper so the handler never does:

```sql
CREATE FUNCTION api.try_cast_uuid(p_input text)
RETURNS uuid
LANGUAGE plpgsql IMMUTABLE
AS $$
BEGIN
    RETURN p_input::uuid;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN NULL;
END;
$$;
```

That is a reasonable trade — one contained, single-purpose exception block versus ad-hoc ones scattered through every handler — and for some types you can avoid it entirely with a format pre-check. A regex-guarded `CASE` handles UUIDs in pure SQL, no savepoint at all. Integers need one more step, and it's a trap worth naming: a regex checks *syntax*, not *range* — `'99999999999999999999'` matches `\d+` and still overflows `int` with SQLSTATE `22003` — so guard the digits with a regex, then bound the value through `numeric` (arbitrary precision) before the final cast. Version-gate the recommendation: on 16+, reach for `pg_input_is_valid` first.

Either way, the handler-side contract is the same: every piece of user input crosses from `text` to its type through a function that returns `NULL` on garbage, and `NULL` routes to a structured 4xx.

## Failure mode 3: the right check in the wrong layer

The third pattern is subtler than the first two and did the most expensive damage in the field: agents put *checks* where they were convenient rather than where they were sound.

Two directions, same root cause. Downward, handlers duplicated state probes the kernel had to redo — "check the record is pending, then call the kernel," where the kernel checked again, or worse, didn't. Upward, kernels did read-check-write with no locking discipline at all. The reviews confirmed a handful of lost-update races of identical shape in the payment path — two concurrent writers both passing validation and both advancing the same record; concurrent batch runs creating duplicate downstream rows — every one of them missing the same idioms:

```sql
-- Agent-written: read, check, write. Two sessions interleave freely.
SELECT status, step_no INTO v_status, v_step
  FROM core.task WHERE id = p_id;                  -- no lock
IF v_status <> 'pending' THEN RETURN NULL; END IF;
UPDATE core.task SET step_no = v_step + 1
 WHERE id = p_id;                                   -- no guard
```

Two independent fixes exist, and either closes the race by itself:

```sql
-- Pessimistic: take the row lock, re-check under it.
SELECT * INTO v_row
  FROM core.task WHERE id = p_id FOR UPDATE;
IF v_row.status <> 'pending' THEN RETURN NULL; END IF;
UPDATE core.task
   SET step_no = v_row.step_no + 1, status = 'in_review'
 WHERE id = p_id
RETURNING * INTO v_row;
RETURN v_row;

-- Optimistic: make the write itself state-conditional. No lock needed.
UPDATE core.task
   SET step_no = step_no + 1, status = 'in_review'
 WHERE id = p_id AND status = 'pending'
RETURNING * INTO v_row;
RETURN v_row;                      -- NULL if someone raced us → 409
```

The lock serializes writers, so the re-check holds for the rest of the transaction (under read committed, the locked read sees the latest committed version of the row; under repeatable read or serializable, a conflicting concurrent update surfaces as `40001` instead — which, as we'll see, must be left to propagate). The guard predicate makes the write itself state-conditional: because the mutation flips the very column the predicate tests, the race's loser matches zero rows, gets `NULL`, and maps to a clean 409 — no lock at all. (A mutation that leaves the guarded column untouched — a pure counter increment, say — relies instead on the `UPDATE`'s own atomicity to avoid the lost update.) Combining lock and guard is defense-in-depth: the guard is redundant while the lock is present, and load-bearing the day a refactor removes it.

Worth stating plainly, because it indicts process rather than model: the project's hundred-plus SQL test files contained zero concurrency tests. Test-driven development — human or agent — cannot see these races, because nobody writes a red test for an interleaving they haven't imagined. The discipline has to be structural: *state-dependent mutations live in the kernel, and the write itself is guarded — by a row lock, a state predicate, or both.* Handlers probe only for things that produce different HTTP status codes, and treat even those probes as advisory — the kernel's guarded return value is the authority.

## The discipline: four phases, every path is a RETURN

The codebase's healthy majority — and the reason its defects were visible at all — followed a uniform handler shape. The reviewers' most striking observation was that every violation they found *stood out because it broke the pattern*. The pattern generalizes to any PostgreSQL-backed API, with or without any particular tooling; here it is as a discipline.

A handler — the function that receives transport input and returns a response — has exactly four phases:

1. **Materialize.** Extract every input into a typed local variable through a safe cast. Nothing downstream ever touches raw `text` from the request.
2. **Validate.** Check required fields are present and optional fields well-formed. Each failure is one `RETURN` of a structured error — 400 for a malformed path parameter, 422 for a body that parses but doesn't validate.
3. **Probe.** Confirm the things that determine the status code: the target exists (else 404), the referenced entity exists (else 422 — the caller pointed *to* something missing, rather than acting *on* something missing), no conflict (else 409).
4. **Execute.** Call the kernel function that owns the atomic mutation, and format its return value. The kernel receives only typed, validated input, and returns the affected row — or `NULL`, which the handler maps to the appropriate conflict or not-found response.

![The four-phase handler: a request flows through materialize, validate, probe, and execute; every failure leaves as a structured RETURN; the kernel owns the guarded atomic mutation; serialization failures bypass everything, uncaught](/pgmi/docs/diagrams/a01-four-phase-handler.drawio.svg)

```sql
CREATE FUNCTION api.patch_alert_seen(p_path jsonb)
RETURNS jsonb
LANGUAGE plpgsql
AS $$
DECLARE
    v_id  uuid;
    v_row core.alert;
BEGIN
    -- materialize
    v_id := CASE WHEN pg_input_is_valid(p_path->>'id', 'uuid')
                 THEN (p_path->>'id')::uuid END;

    -- validate
    IF v_id IS NULL THEN
        RETURN jsonb_build_object('status', 400,
            'body', jsonb_build_object('title', 'Bad Request',
                                       'detail', 'id must be a UUID'));
    END IF;

    -- probe + execute: this endpoint has nothing to probe that the
    -- kernel's guarded return doesn't answer, so phase 3 collapses
    -- into phase 4 (a referenced-entity or conflict check would sit here)
    v_row := core.mark_alert_seen(v_id);

    IF v_row IS NULL THEN
        RETURN jsonb_build_object('status', 404,
            'body', jsonb_build_object('title', 'Not Found'));
    END IF;

    RETURN jsonb_build_object('status', 200, 'body', to_jsonb(v_row));
END;
$$;
```

(Self-contained for the article; in a real system the response constructor is shared and emits `application/problem+json`.)

The load-bearing rule: **every path out of a handler is a `RETURN`, never a `RAISE`.** Errors the handler *owns* — validation failures, missing targets, conflicts — travel as values, in a structured error format. The natural target shape is RFC 9457 (Problem Details for HTTP APIs, the Standards-Track successor to RFC 7807): a `title`, a `detail`, and — as extension members, which the format is explicitly designed to carry — a machine-readable `code` and per-field validation errors. This is not an invented convention — Spring Framework 6 ships `ProblemDetail` natively, Zalando's REST guidelines mandate the format for all error responses, and `application/problem+json` is a registered media type. A PostgreSQL function can build one in a single `jsonb_build_object`.

There is respectable independent support for the errors-as-values stance in the PostgreSQL world. YugabyteDB's PL/pgSQL documentation (authored by Bryn Llewellyn) makes the "hard shell" argument: don't let raw database errors escape to client code — record the diagnostics and "return a suitably encoded response" instead, not least because raw errors leak schema names, constraint names, and internal structure to clients. The four-phase discipline is that argument, applied one function at a time.

## The steelman: doesn't this discard SQLSTATE?

The serious objection to every-path-is-a-RETURN: SQLSTATE is a machine-readable error taxonomy with decades of client support, and converting errors to values throws it away. It deserves a real answer, in two parts.

For errors the handler owns, the objection dissolves on inspection: the structured payload *carries* the machine-readable code — as a `code` extension member, or the SQLSTATE itself from `pg_input_error_info` — without the parts of the exception you never wanted to ship, like an `SQLERRM` that reads `insert or update on table "line_item" violates foreign key constraint "line_item_order_id_fkey"`. A raised exception gives you a machine-readable code plus schema leakage; a problem response gives you the code alone.

But some errors are *not the handler's to own*, and this is where the discipline needs its one carve-out stated as sharply as the rule itself. Serialization failures (`40001`) and deadlocks (`40P01`) belong to whoever owns the transaction — the client that issued `BEGIN` — because the only correct response is to retry the *whole transaction*, and only the transaction owner can do that. PostgreSQL's own documentation on serialization failure handling says applications must be prepared to retry; the retry loop cannot live in PL/pgSQL, and the reason is worth internalizing: under repeatable read and serializable, the snapshot is taken at the transaction's first statement and then held. An in-function "retry" after rolling back to a savepoint re-reads the same frozen snapshot, reaches the same conclusion, and conflicts the same way, indefinitely.

Catching `40001` in a handler is worse than useless. The exception block's savepoint rolls back the failed statement's writes, the handler returns some tidy error response — or worse, a success — and the surrounding transaction goes on to *commit*. The client is told everything is fine while the contested write has silently vanished. To be fair to the agents: the field reviews did not catch one doing this — but the naive catch-everything handler is exactly one `WHEN OTHERS` away from it, which is a reason the ban on handler exception blocks earns its absolutism.

So the honest, complete rule is a hybrid:

- **Return** the errors you own: validation, missing targets, conflicts — as structured values with machine-readable codes.
- **Never catch** the errors the client owns: `40001`, `40P01`, and their transaction-rollback kin must propagate with SQLSTATE intact, because that SQLSTATE is the one bit that tells the client to retry rather than give up.
- **Let genuinely unexpected errors escape too** — an unanticipated exception is a server bug, and a gateway that sanitizes it into a bare 500 (message logged, not shipped) is more truthful than a handler that launders it into a plausible-looking 400.

## Where this sits in the validation debate

"Validate at the database boundary" sometimes draws the response that validation belongs in the application layer. The mainstream position has largely converged on defense-in-depth — Dian Fay's formulation is the memorable one: "database constraints are law; application logic constraints are advice." The database is the one layer every consumer must pass through; a second application, a manual `psql` session, and an ORM escape hatch all bypass app-layer checks and none bypass a `CHECK` constraint.

The traditional cost of relying on the law was error ergonomics: a raw constraint violation surfaces as one generic, user-hostile exception. That is the gap this discipline closes. Soft casts and pre-statement validation produce per-field, structured, client-safe errors *at the database boundary*, while constraints remain underneath as the backstop for whatever validation missed. You keep the fortress and get the friendly error messages.

## Honest limits

**Where exception blocks are correct.** Inside a pre-16 `try_cast`, as discussed. In deploy-time orchestration and migration scripts, where a savepoint per step is exactly what you want and requests-per-second is irrelevant. In background and queue processing, where "log and continue past the poison message" is the right shape. The ban is on exception blocks *in request-path handlers*, not in PL/pgSQL.

**What the discipline costs.** Handlers get longer — four phases with one `RETURN` per outcome is more lines than `EXCEPTION WHEN OTHERS THEN RETURN error()`. The kernel/handler split is a real architectural commitment, and it presumes your schema carries invariants as constraints rather than as procedural checks. If your API surface is two endpoints, this is ceremony.

**What it does not fix.** Concurrency. The four phases make handlers honest, but the race fixes in failure mode 3 — `FOR UPDATE`, guard predicates, `ON CONFLICT` — are a separate discipline, and no test suite the agents wrote would have caught their absence. If agents write your money-moving code, someone who thinks in interleavings must read it.

**What this evidence is.** Two thorough reviews of one production codebase, plus corroborating patterns in a second smaller one. That is field data, not a benchmark; the per-pattern counts above are real observations from this codebase, not extrapolations, and indicative of nothing beyond agents-writing-PL/pgSQL generally except that the failure modes are systematic, not random. The strongest evidence for the discipline is internal: in a couple dozen handler files built by the same agents under the same instructions, the defects clustered precisely in the deviations from it.

## Where pgmi comes in

I maintain [pgmi](https://github.com/vvka-141/pgmi), a PostgreSQL-native deployment tool whose advanced project template ships this discipline as working code, because — as the field reviews showed — agents follow the pattern they can see. The template's `common.try_cast` family does the safe casts (regex-guarded pure SQL where a format check suffices, a range check through `numeric` where it doesn't, and a contained exception block only where parsing truly needs one — the helper absorbs the cost so handlers never carry it). `api.problem_response` builds the RFC 9457 responses. The shipped handlers contain zero `EXCEPTION` blocks, and the gateway deliberately lets `40001`/`40P01` propagate with SQLSTATE intact while sanitizing everything else. The doctrine itself ships in the binary as an AI-readable skill — `pgmi ai skill pgmi-handler-patterns` prints it, so a coding agent working in a pgmi project reads the same rules this article just argued for.

None of it requires pgmi to adopt; the template exists so you don't have to build the reference implementation before an agent can imitate it.

## Takeaway

AI agents don't write bad SQL at random. They port the idioms of exception-driven, cast-trusting, validate-anywhere languages into a runtime where control flow allocates savepoints, casts abort transactions, and the only checks that hold under concurrency are the ones welded to the statement. The defense is not better prompting — it is a visible, uniform discipline in the codebase: safe casts at the boundary, four phases, every path a `RETURN`, locks and guard predicates in the kernel, and `40001` sacred. Agents imitate what they see. Give them something worth imitating.

---

## Sources

- PostgreSQL documentation: [Trapping Errors (PL/pgSQL control structures)](https://www.postgresql.org/docs/current/plpgsql-control-structures.html) — exception blocks and their cost; [Data Validity Checking Functions](https://www.postgresql.org/docs/current/functions-info.html) — `pg_input_is_valid`, `pg_input_error_info`; [Subtransactions](https://www.postgresql.org/docs/current/subxacts.html); [Serialization Failure Handling](https://www.postgresql.org/docs/current/mvcc-serialization-failure-handling.html)
- GitLab: [Why we spent the last month eliminating PostgreSQL subtransactions](https://about.gitlab.com/blog/2021/09/29/why-we-spent-the-last-month-eliminating-postgresql-subtransactions/)
- postgres.ai: [PostgreSQL Subtransactions Considered Harmful](https://postgres.ai/blog/20210831-postgresql-subtransactions-considered-harmful)
- Cybertec: [Subtransactions and performance in PostgreSQL](https://www.cybertec-postgresql.com/en/subtransactions-and-performance-in-postgresql/)
- IETF: [RFC 9457 — Problem Details for HTTP APIs](https://www.rfc-editor.org/rfc/rfc9457.html); [Zalando RESTful API Guidelines](https://github.com/zalando/restful-api-guidelines/blob/main/chapters/http-status-codes-and-errors.adoc); [Spring Framework error responses](https://docs.spring.io/spring-framework/reference/web/webmvc/mvc-ann-rest-exceptions.html)
- YugabyteDB PL/pgSQL documentation (authored by Bryn Llewellyn): [The exception section](https://docs.yugabyte.com/stable/api/ysql/user-defined-subprograms-and-anon-blocks/language-plpgsql-subprograms/plpgsql-syntax-and-semantics/exception-section/) — the "hard shell" argument
- Validation debate: [Data constraints: database layer or app logic?](https://dev.to/shalvah/data-constraints-database-layer-or-app-logic-3j83) (incl. Dian Fay's "law vs. advice"); [Lobsters discussion](https://lobste.rs/s/4zmaos/how_much_logic_should_i_keep_at_database_vs)
- Adjacent work: Vibhor Kumar, [Postgres as an execution environment for AI (ORBIT)](https://vibhorkumar.wordpress.com/2026/05/28/postgres-as-an-execution-environment-for-ai-failure-modes-hooks-and-the-orbit-framework/) — the mirror-image problem: Postgres calling *out* to AI services safely, where this article is about AI writing the code that runs *inside* Postgres; Readyset, ["Why LLMs write incorrect SQL"](https://readyset.io/blog/why-llms-write-incorrect-sql-and-what-that-means-for-your-database) (declarative-only prior art)
- All SQL examples verified on PostgreSQL 18.4; the `pg_input_error_info` output and the integer-overflow behavior additionally verified on 16.x.
