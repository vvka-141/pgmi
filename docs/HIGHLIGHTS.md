---
title: "Highlights"
description: "Ten distinctive pgmi capabilities, each grounded in the implementation or a guide."
weight: 25
---

# Highlights

[Why pgmi?](WHY-PGMI.md) explains when the approach fits. [Tradeoffs](TRADEOFFS.md)
is the honest cost list. This page is the third side: **ten capabilities that
distinguish pgmi's model**, each with a pointer to the implementation or guide.

Items 1 to 7 are **pgmi core** — they work in any project. Item 8 is both core
and template. Items 9 and 10 are **the advanced template** — application code
you own after `pgmi init --template advanced`, not part of the binary.

---

## 1. The test gate is the transaction, not a separate command

Migrations, database tests, and the commit decision are one PostgreSQL transaction
against the **target** database. A failing test doesn't fail a separate build step:
it aborts the deployment that was already half-applied and rolls back its
transactional schema and data changes. PostgreSQL sequence advances and effects
outside the transaction are not rolled back.

```sql
BEGIN;
  -- apply migrations
CALL pgmi_test();      -- each test rolls back to its directory's fixture state
COMMIT;                -- reached only if every test passed
```

Other tools test migrations too. Atlas runs
[`migrate test`](https://atlasgo.io/testing/migrate) against a dev database.
Flyway documents
[transactional assertions embedded in a migration](https://www.red-gate.com/hub/product-learning/flyway/test-driven-development-flyway),
including a switch that raises an error and rolls the transaction back. pgmi's
distinction is the combination of a separate hierarchical test tree, per-test
savepoint isolation, callback events, and a gate invoked by your `deploy.sql`
inside the target deployment transaction.

→ [Testing guide](TESTING.md) · runnable, CI-verified: [`examples/test-gated-deploy/`](https://github.com/vvka-141/pgmi/tree/main/examples/test-gated-deploy)

---

## 2. Your test tree is a transaction tree — and it emits an event stream

`__test__/` directories nest. `pgmi_test_plan()` walks them depth-first in pure SQL:
each directory's `_setup.sql` fixture runs once for the whole subtree, each test rolls
back to its directory's savepoint, and each subtree tears down on exit. A child test sees its
parents' fixtures; no test sees another test's transactional writes. Sequence
advances and external effects are outside savepoint rollback.

Test execution also emits a typed event stream — `suite_start`, `fixture_start`,
`test_start`, `test_end`, `rollback`, `teardown_end` — as a `pgmi_test_event`
composite. The default callback raises NOTICEs; pass your own and the same run
produces TAP, JUnit XML, a row per test in a results table, or an OpenTelemetry span:

```sql
CALL pgmi_test(NULL, 'pg_temp.my_reporter');
```

→ [Testing guide](TESTING.md) · [Custom test callbacks](TESTING.md#custom-test-callbacks) · [TAP 14 reporter](../examples/tap-reporter/)

---

## 3. The deployment plan is a query result, so you can assert on it

`pgmi_plan_view` is a view. That means the plan is inspectable **and testable**
before anything executes — by your SQL, expressing your policy:

```sql
-- Abort if the plan drifted from the manifest reviewed in the PR
IF EXISTS (SELECT path, execution_order FROM pg_temp.pgmi_plan_view
           EXCEPT SELECT path, execution_order FROM reviewed_manifest) THEN
    RAISE EXCEPTION 'execution plan drifted from the reviewed manifest';
END IF;
```

Atlas lints a generated plan against vendor-supplied analyzers, which is genuinely
strong. The difference is authorship: pgmi's plan is data your project writes its own
rules against — "no two files may claim the same sort key", "nothing may run before
the tenancy migration", "the plan must match this signed manifest" — without waiting
for the tool to support the rule.

→ [Metadata guide](METADATA.md) · [Session API](session-api.md) · runnable, CI-verified: [`examples/execution-order-policy/`](https://github.com/vvka-141/pgmi/tree/main/examples/execution-order-policy)

---

## 4. Checksums that survive a reformat

Every file arrives with two checksums:

| column | content |
|---|---|
| `pgmi_source_view.checksum` | SHA-256 of the raw bytes |
| `pgmi_source_view.pgmi_checksum` | SHA-256 of **normalized** content — comments stripped, case folded and whitespace collapsed outside literals, quoted identifiers and dollar-quoted bodies |

`pgmi_plan_view.pgmi_checksum` is the same normalized value.

A raw-byte checksum treats "I added a comment explaining this migration" as a
different migration. Flyway reports such a change as a `checksum mismatch`,
and its documented remedy is `flyway repair`, which rewrites history to match
the file.

pgmi's normalized checksum doesn't move when you reformat, re-indent, add
documentation, or edit a `<pgmi-meta>` block (it lives in a comment). Track against
`pgmi_checksum` and reformatting is free; track against `checksum` when you want
byte-exact provenance. Both are on every row; the choice is yours per use case.

The honest cost: a change *only* to a comment is invisible to `pgmi_checksum` — which
is wrong if your comments carry meaning, e.g. planner hints.

→ [Two checksums, and which to track against](session-api.md#two-checksums-and-which-to-track-against) · [Checksum-based change detection](DEPLOY-GUIDE.md#checksum-based-change-detection)

---

## 5. One file, several positions in the plan

A file's `<pgmi-meta>` block may declare more than one sort key, and the plan
`UNNEST`s them — so the same file executes at several stages of one deployment:

```xml
<sortKeys>
  <key>000/020</key>  <!-- bootstrap: create the roles -->
  <key>005/010</key>  <!-- after the load: re-grant on new objects -->
</sortKeys>
```

In a file-per-change tool, the second run means a second file — a copy that must be
kept in sync with the first by hand, forever. Here it is one idempotent file listed
twice.

→ [Multi-phase execution](METADATA.md#multi-phase-execution) · [`examples/execution-order-policy/`](https://github.com/vvka-141/pgmi/tree/main/examples/execution-order-policy)

---

## 6. Most of the CI gate needs no database at all

Project structure, metadata validity, UUID uniqueness and the full execution order are
computable from the files alone — so they are, with JSON output for pipelines:

```bash
pgmi info .              --json    # structure, template, test coverage
pgmi metadata validate . --json    # XML validity, duplicate ids
pgmi metadata plan .     --json    # the execution order this project will produce
```

No connection string, no container, no dev database. A pull request can be rejected
for a plan-ordering mistake in a job that takes a second.

→ [CLI reference](CLI.md) · [CI/CD guide](CICD.md)

---

## 7. `CREATE INDEX CONCURRENTLY` works inside a deploy

Some deployment tools require per-script configuration for statements that cannot
run in a transaction. Flyway, for example, supports
[`executeInTransaction=false`](https://documentation.red-gate.com/fd/migration-transaction-handling-273973399.html).
pgmi's [execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)
splits deploy.sql at the first top-level `COMMIT`: everything before it is
one atomic transaction (your test gate lives here); everything after it runs
statement-by-statement with autocommit, exactly like psql. Put
`CREATE INDEX CONCURRENTLY` in the tail; the transaction boundary remains visible
in SQL, with no framework-specific per-script setting or second tool invocation.

→ [Execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract) · [Lock-safe deploy example](https://github.com/vvka-141/pgmi/tree/main/examples/lock-safe-deploy)

---

## 8. Agent-native by construction, not by a docs site

*Both: the binary and the basic and advanced templates.* The binary carries its own machine-readable documentation, so a coding agent can learn
pgmi's conventions from the tool it is already invoking:

```bash
pgmi ai                 # llms.txt-style overview
pgmi ai contract        # the session API contract, machine-readable
```

`pgmi init` is the part that matters: every basic or advanced project carries an `AGENTS.md`
routing table and skills under `.agents/skills/`, committed with the code, so the next
agent to open the project learns how it deploys *before* it edits SQL — rather than
inventing a migration framework that isn't there. The skills include PostgreSQL recipes
whose SQL pgmi's CI deploys (PostgreSQL 15 for the basic template, 18 for both).

→ [AI assistant support](../README.md#ai-assistant-support) · [`llms.txt`](https://vvka-141.github.io/pgmi/llms.txt)

---

## In the optional advanced template

The next two items are application code the advanced template writes into your
project, not part of the binary.

## 9. Routes declare their transaction policy, and the policy is resolved before `BEGIN`

*Advanced template.* A handler declares what it needs:

```sql
'minTransactionIsolation', 'serializable',
'readOnly', true
```

and the gateway resolves the policy **before opening the transaction**:

- **isolation = `max(route floor, client-requested)`** — a client may escalate, never
  downgrade. A downgrade below the floor is not rejected at runtime; it is
  structurally unrepresentable.
- **`DEFERRABLE` derived** for `SERIALIZABLE READ ONLY`. Such a transaction waits for
  a conflict-free snapshot at `BEGIN` and can then never abort with `40001` — those
  routes need no retry logic at all.
- **`x-pgmi-replica-safe` published in OpenAPI**, and it is honest: `readOnly` alone
  is *not* sufficient to offload to a standby, because a hot standby caps at
  `repeatable read`. A read-only route with a `serializable` floor emits
  `replica-safe: false`.
- **Fail-closed in the database.** A proxy that skips the policy lookup gets
  `428 Precondition Required`, not a silently weaker transaction.
- **`40001` / `40P01` propagate with SQLSTATE intact** instead of being flattened into
  a generic 500, so a client can tell "retry me" from "this handler is broken."

PostgREST sets isolation at the role or function level — static server-side
configuration, with no per-route floor, no client negotiation, no read-only route
declaration, no `DEFERRABLE` derivation and no replica hint. We are not aware of
another framework that publishes a route's isolation policy in its OpenAPI document.

→ [Transaction policy](advanced/TRANSACTION-POLICY.md) · [Advanced template overview](advanced/README.md#declarative-per-route-transaction-policy) · [`lib/api/00-transaction-isolation.sql`](https://github.com/vvka-141/pgmi/blob/main/internal/scaffold/templates/advanced/lib/api/00-transaction-isolation.sql)

---

## 10. An MCP server that *is* a database transaction, not a process in front of one

*Advanced template.* This is runtime MCP your application serves, not
`pgmi serve`, which exposes pgmi's own CLI to coding agents
([CLI reference](CLI.md#pgmi-serve)). Every PostgreSQL MCP server we found is an external process
(Python, TypeScript) that exposes **the database** to an agent: run SQL, inspect the
schema, analyze a plan. The advanced template implements the MCP JSON-RPC protocol
**in SQL** and exposes **your domain operations** as tools:

- Tools, resources and prompts register in the same `api.handler` registry as REST
  routes and JSON-RPC methods, one registration per protocol, and can share one
  kernel with them.
- One tool call is one transaction, at the isolation the tool declared (item 9).
- **Tool discovery is auth-aware**: `api.mcp_list_tools` lists only the tools the session's
  caller may call, so an agent's visible capability set is scoped to its identity.
- Row-level security applies to the agent exactly as it applies to a human caller.
- The agent has its own principal and an agent role its person chose; it holds the
  role's permissions intersected with the person's, and every log records both, so an
  agent's change is never mistaken for its person's.

The difference is the blast radius. "Give the agent SQL access" and "give the agent
seven audited, transactional, RLS-scoped operations" are not the same security
posture.

→ [MCP gateway](advanced/MCP.md) · [Author MCP handlers](advanced/MCP-HANDLERS.md)

---

## What this page deliberately doesn't claim

- **Not "pgmi is better than Flyway."** For "run these numbered files in order,"
  Flyway has a shallower learning curve and pgmi's advantages don't apply. See
  [Tradeoffs](TRADEOFFS.md).
- **Not "no tool tests migrations."** Atlas and Flyway both have real testing stories;
  item 1 is about *where the gate sits*, not about whether tests exist.
- **Not a safety tier.** The advanced template is more infrastructure, not more
  production-ready than basic.

## See also

- [Why pgmi?](WHY-PGMI.md) — when the approach fits
- [Tradeoffs](TRADEOFFS.md) — the honest cost list
- [Coming from Flyway/Liquibase/Sqitch](COMING-FROM.md) — migration guides
