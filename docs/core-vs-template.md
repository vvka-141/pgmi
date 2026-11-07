---
title: "What pgmi gives you, and what you own"
description: "The four parts of a pgmi project: pgmi core, the template you started from, the agent guidance written into the project, and the application features of the advanced template."
weight: 25
---

# What pgmi gives you, and what you own

pgmi has one job: prepare a PostgreSQL session and execute your `deploy.sql`.
Everything on the pgmi side of that line upgrades when you upgrade pgmi.
Everything on the project side is yours to keep, change or replace.

A pgmi project has four parts. Only the first one is pgmi.

| Part | What it is | Who owns it | Runs when |
|---|---|---|---|
| [pgmi core](#pgmi-core) | The binary, its session API, the execution contract, the test macro, `pgmi ai`, `pgmi serve`, the Go library | pgmi; upgrades with the binary | Deploy time |
| [Template code](#template-code) | The files `pgmi init` wrote: minimal, basic or advanced | You, from the moment it lands | Deploy time, through your `deploy.sql` |
| [Agent guidance](#agent-guidance) | `AGENTS.md`, `CLAUDE.md` and `.agents/skills/` (basic and advanced only) | You | While an agent develops the project |
| [Application features](#application-features) | The advanced template's handlers, sessions, row-level security and gateways | You | Runtime, on every request |

## pgmi core

The Go binary, and the session contract it creates before your `deploy.sql`
runs. This is what changes when you install a new pgmi.

The session contract consists of:

| Layer | Objects |
|-------|---------|
| Internal tables | `_pgmi_parameter`, `_pgmi_runtime`, `_pgmi_source`, `_pgmi_source_metadata`, `_pgmi_test_directory`, `_pgmi_test_source` |
| Public views | `pgmi_source_view`, `pgmi_parameter_view`, `pgmi_runtime_view`, `pgmi_plan_view`, `pgmi_test_source_view`, `pgmi_test_directory_view`, `pgmi_source_metadata_view` |
| Functions | `pgmi_test_plan(p_pattern)`, `pgmi_test_generate(p_pattern, p_callback)`, `pgmi_register_file(...)`, `pgmi_test_callback(event)` |
| Preprocessor macro | `CALL pgmi_test()` — expanded by Go before SQL reaches PostgreSQL |
| Types | `pgmi_test_event` composite type for test lifecycle callbacks |

The rest of core:

- **The execution contract.** Atomic head, then psql tail: the binary decides
  how your SQL is sent to PostgreSQL. It follows the binary version, not
  `--compat` ([the execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract)).
- **The test runner.** pgmi discovers `__test__/` and `__tests__/`
  directories and their `_setup.sql` fixtures. `CALL pgmi_test()` expands into
  the savepoints and fixture calls for that tree. *Whether* tests gate the
  commit is up to `deploy.sql`: they run where it calls the macro, and nowhere
  else ([Testing](TESTING.md)).
- **The CLI.** `deploy`, `init`, `info`, `metadata`, `templates`, `config`, `ai`
  and `serve` ([CLI reference](CLI.md)).
- **Tooling for coding agents.** `pgmi ai` and `pgmi ai contract` print what an
  agent needs to know about this binary's session API. `pgmi serve` offers the
  CLI commands as MCP tools over stdio, so an agent can deploy a project and check its metadata and plan.
- **The Go library.** `pkg/pgmi/deploy` runs the same deploy from your own Go
  program ([Go library](GO-LIBRARY.md)).

pgmi keeps no migration history of its own. What has run, and what may run
again, is decided by your `deploy.sql`. That is where the templates come in.

## Template code

`pgmi init` copies one template into your project. Each is a starting point,
not a framework: pgmi never checks that your project still follows it.

- **minimal** (the default): a `deploy.sql` loop that runs every SQL file in
  path order, `hello_world.sql` and a `.pgmiignore`. The core mechanism, bare.
- **basic**: conventional two-layer development, with the application outside PostgreSQL. Run-once tables in `physical/`, tracked
  in a ledger that refuses an edited file. Views and functions in `semantic/`,
  re-run on every deploy. Tests that gate the commit. Your application, in any
  language, calls the functions over SQL.
- **advanced**: an application backend in PostgreSQL. A deploy history, an
  exchange log, membership with organizations, and the
  [application features](#application-features) below.

[Choose a template](templates.md) compares them side by side.

## Agent guidance

The basic and advanced templates also write an `AGENTS.md` (a routing table
for coding agents), a `CLAUDE.md` that imports it, and skills under
`.agents/skills/`. These are project files, not part of pgmi: commit them and
change them to match your conventions. A pgmi upgrade does not touch them.
The facts that must match the binary come from `pgmi ai contract` instead.

## Application features

Only the advanced template has a runtime part. Its handler registry serves
REST, JSON-RPC and MCP from SQL. Gateways under `gateways/` handle HTTP and
sign-in, then run each request as one database transaction. These are your application's features, and pgmi has no part in
them at runtime. Two MCP surfaces exist, and they are unrelated:

| | `pgmi serve` | The advanced template's MCP gateway |
|---|---|---|
| Part of | pgmi core | Your application |
| Who calls it | A coding agent developing the project | AI assistants using the deployed application |
| What it exposes | pgmi's own commands | The tools, resources and prompts you write in SQL |
| Transport | stdio | HTTP |

## The rule

**pgmi's job ends at preparing the session and executing your `deploy.sql`.
Everything reachable from `deploy.sql` is yours.**

A pgmi upgrade never upgrades, migrates or overwrites template code. Trimming
the advanced template is expected. The gateways, `api/dev/` and several
framework parts delete cleanly. Membership is a ready-made access model you can
keep, reshape or replace; `lib/` calls its identity readers, so replacing it is
a refactor rather than a folder delete
([Before you choose it](advanced/README.md#before-you-choose-it)).

## What happens on upgrade

**Core session API** — may change in a minor release before pgmi 1.0. A view, column or function can be added, renamed or removed; every such change is listed under **Upgrading** in that release's notes ([RELEASES.md](https://github.com/vvka-141/pgmi/blob/main/RELEASES.md)). `--compat` selects a contract version the binary ships (only `1` today) and rejects one it does not; it does not freeze columns across pgmi releases. Pin the pgmi binary version: that pins the session API and its behavior (plan ordering, which files load, the execution contract). Read Upgrading before you move the pin.

**Your project files** — pgmi never writes, diffs or changes them. It loads
them into session tables and runs your `deploy.sql`. The one rewrite is in
memory: `CALL pgmi_test()` becomes the generated test SQL before `deploy.sql`
is sent.

## Which docs apply to you

| You are... | Read these |
|------------|-----------|
| Any pgmi user | [Quickstart](QUICKSTART.md), [Session API](session-api.md), [deploy.sql guide](DEPLOY-GUIDE.md), [CLI reference](CLI.md), [Testing](TESTING.md) |
| Using the basic template or your own project shape | The above, plus [Script metadata](METADATA.md) when you need execution ordering |
| Using the advanced template | Everything above, plus [the advanced template section](advanced/README.md) — REST, MCP, transaction policy, client generation |

The [advanced template overview](advanced/README.md) states the boundary in full detail and documents every subsystem you inherit.
