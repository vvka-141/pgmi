---
title: "Why no orchestration flags"
description: "Design record: why pgmi's CLI has no --dry-run, --rollback, or --skip flags — and the SQL-side equivalent of each rejected flag."
weight: 30
---

# Design Record: Why No Orchestration Flags

> **Status: STANDING POLICY** — applied to every CLI proposal since the
> beginning. The CLI's actual surface is in the [CLI reference](../CLI.md).

## Decision

pgmi's CLI handles **infrastructure only**: connections, authentication,
parameters, observability, confirmation bypass. Every flag that would decide
*what the deployment does* is rejected, because deployment behavior is
declared in the project's `deploy.sql` — where it is programmable, versioned,
and testable.

## The rejected flags, and where each one lives instead

Each of these was genuinely proposed or expected (they exist in comparable
tools) and rejected on the same grounds: a flag is a fixed policy; SQL is a
programmable one.

| Rejected flag | The SQL that replaces it |
|---|---|
| `--dry-run` | A parameter your deploy.sql interprets — see the worked example below |
| `--rollback` | Your transaction strategy: abort before `COMMIT`, or run your own compensating scripts — [rollback strategies](../PRODUCTION.md#rollback-strategies) |
| `--skip-*` / `--only-*` | A `WHERE` clause on [`pgmi_plan_view`](../session-api.md) — selection is a query |
| `--quiet` / verbosity tiers | `RAISE NOTICE`/`RAISE DEBUG` in your SQL plus `client_min_messages`; the CLI only forwards PostgreSQL's stream |
| a `pgmi test` subcommand | `CALL pgmi_test()` *inside* `deploy.sql` — [tests gate the same transaction](../TESTING.md) that applies the changes, which a separate command could never do |

## The canonical worked example

`--dry-run` is the strongest candidate flag, so it makes the best
demonstration that the flag is **unnecessary, not merely unwanted**. A
parameter plus three lines of SQL implements it — with semantics the project
controls, not semantics a tool guessed:

```sql
-- deploy.sql runs inside one transaction; abort it to preview.
DO $$
BEGIN
    IF COALESCE(current_setting('pgmi.preview', true), 'false') = 'true' THEN
        RAISE EXCEPTION 'preview mode: rolling back, no changes applied';
    END IF;
END $$;
```

```bash
pgmi deploy . -d mydb --param preview=true   # everything runs, nothing commits
```

A built-in `--dry-run` would have to choose one meaning (parse only? execute
and roll back? print the plan?) for every project. The parameter version
chooses per project, and the choice is reviewable in version control like
any other line of the deployment.

## Consequence

The CLI stays small and stable, and the answer to "can pgmi do X during a
deployment?" is almost always "yes — write it in deploy.sql" rather than a
feature request. The cost is symmetrical and real: pgmi will not stop you
from writing a bad deployment program —
[the trade-offs page](../TRADEOFFS.md) owns that honestly.

## See also

- [Why execution fabric](why-execution-fabric.md) — the philosophy this policy enforces
- [deploy.sql guide](../DEPLOY-GUIDE.md) — the patterns that replace the flags
