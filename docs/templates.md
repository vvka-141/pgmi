---
title: "Choose a template"
description: "Compare the minimal, basic and advanced templates: who each is for, what each ships, what each leaves out, and why the generated code is yours."
weight: 32
---

# Choose a template

`pgmi init` writes a starting project from a template. A template is scaffolding, not part of pgmi. Projects that mix run-once and re-runnable files often need the same infrastructure: remember which scripts already ran, and do not run them again. The basic and advanced templates supply that ledger as plain SQL you can read and change, and call pgmi's built-in `CALL pgmi_test()` before their `COMMIT`; minimal leaves both to you. pgmi itself only runs your `deploy.sql`, and the minimal template shows it bare.

## Side by side

| | minimal | basic | advanced |
|---|---|---|---|
| **For** | Building your own deployment from nothing | Conventional database development, with the application outside PostgreSQL: tables, views, and functions called by name | Building an application backend in PostgreSQL, with security, performance and REST/RPC/MCP routing in the database |
| **You write** | Any SQL files; `deploy.sql` runs them | Run-once tables in `physical/`, re-run views and functions in `semantic/`, tests in `__test__/` | Tables, kernel functions and handlers in a file contract with `<pgmi-meta>` sort keys; gateways keep only cross-cutting concerns |
| **Ships** | `deploy.sql` (runs every SQL file in path order, one transaction), `hello_world.sql`, and a `.pgmiignore` that keeps parameter files and backup copies out of the session | `deployment.run_once` ledger, refusal of changed run-once files, `CALL pgmi_test()` before `COMMIT`, required `env` parameter (`dev`, `test`, `prod`), `EXECUTE` revoked from `PUBLIC` by default, dev-only seed data | `internal.deployment`, `internal.deployment_script`, `internal.deployment_script_content`, `internal.deployment_script_execution_log`; refusal of changed run-once files; `internal.environment`; exchange log `api.exchange` (with `api.rest_exchange`, `api.rpc_exchange`, `api.mcp_exchange`); `internal.job_run_log`; lost-grant check, test gate before `COMMIT`; parameters declared in `deploy/parameters.xml` and read as `current_setting('pgmi.<key>')` |
| **Is not** | No ledger, sample tests, test-gate call or parameter use supplied: a file that ran runs again. Core tests and parameters work when you add them | No API, gateway or MCP inside the database | A "more production" basic. It is a different, opinionated starting point you trim |
| **PostgreSQL** | 15+ | 15+ | 18+, a role with `CREATEROLE` and `CREATEDB`, no superuser ([requirements](#advanced-template-requirements)) |
| **Start** | `pgmi init myapp` | `pgmi init myapp --template basic` | `pgmi init myapp --template advanced` |

`pgmi templates list` prints the same three with one-line descriptions.

## Advanced template requirements

- PostgreSQL 18+ (`deploy.sql` refuses an older server before creating anything)
- A role with `CREATEROLE` + `CREATEDB`, no superuser. It creates the owner, admin, api and kernel roles and grants itself the ones it uses.
- Extensions: `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore`, `btree_gist` (available on the managed clouds that offer PostgreSQL 18; see the [Production Guide](PRODUCTION.md#managed-cloud-postgresql))

## What every non-minimal template remembers for you

A table file must run once per database; a view or function file can run on every deploy. Both templates keep a ledger of the run-once files and refuse a deploy when one that already ran has changed, before anything runs. The fix for a changed table is a new run-once file, not an edit.

![Before anything runs, both templates refuse a deploy if a run-once file that already ran has changed; then re-run files always run and run-once files are skipped once they have run — basic records only run-once files in deployment.run_once, advanced logs every file in internal.deployment_script_execution_log.](diagrams/d16-run-once-ledger.drawio.svg)

## It's yours

After `pgmi init`, the generated code is your project's source. Change it, replace it or delete it. A pgmi upgrade never reads, changes or overwrites it: the binary only loads your files and runs your `deploy.sql`. To take a later template fix into an advanced project, merge it by hand ([Upgrading](advanced/UPGRADING.md)). The advanced template is pre-1.0: a later release may again need a new database, and template fixes arrive by manual merge. The boundary is spelled out in [What pgmi gives you, and what you own](core-vs-template.md).

## Moving from basic to advanced

There is no in-place switch: the advanced template is a different application with its own deploy.sql, ledger and file contract. To move a basic project:
1. Scaffold the advanced template into a new directory.
2. Copy `physical/` files in as run-once files and `semantic/` files as re-run files, and keep each file to what the file contract in the scaffolded advanced `README.md` allows. Every file needs a sort key at `005/` or later:
   - The `physical/` files already carry a `<pgmi-meta>` block without sort keys: add one by hand to each, `<sortKeys><key>005/<entity>/1</key></sortKeys>`. `pgmi metadata scaffold` leaves a file that has a block alone.
   - The `semantic/` files have no block: `pgmi metadata scaffold <dir> --sort-key 006/ --write` writes one into each, sorted after every table (they read them).
   - Every table is forced to row-level security or exempt from it: add `SELECT core.apply_org_rls('<table>');` (an organization's rows) or `SELECT pg_temp.exempt_from_rls('<table>', '<reason>');` to the run-once file that creates it, and every view is `WITH (security_invoker = true)`. The deploy names any other guard a file breaks, with a HINT.
   - `pgmi metadata plan .` shows the order before you deploy.
3. Deploy into a new database and move the data with your own SQL. The basic `deployment.run_once` ledger does not carry over.

[Script metadata](METADATA.md) explains sort keys.

## Where to go next

- [The basic template](basic/README.md)
- [The advanced template](advanced/README.md)
- [Quickstart](QUICKSTART.md): install pgmi and deploy a first project
- [What pgmi gives you, and what you own](core-vs-template.md)
- [Trade-offs](TRADEOFFS.md): when pgmi is not the right tool
