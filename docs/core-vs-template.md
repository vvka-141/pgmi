---
title: "What pgmi gives you, and what you own"
description: "The boundary between pgmi core (the binary and its session contract) and the scaffolded project code that becomes yours on init."
weight: 25
---

# What pgmi gives you, and what you own

pgmi has exactly one job: prepare a PostgreSQL session and execute your `deploy.sql`. Everything on the pgmi side of that boundary upgrades when you upgrade pgmi. Everything on the project side is yours — to keep, modify, or delete.

## The three artefacts

### pgmi core

The Go binary and the session contract it creates before your `deploy.sql` runs. This is what changes when you run `go install github.com/vvka-141/pgmi/cmd/pgmi@latest`.

The session contract consists of:

| Layer | Objects |
|-------|---------|
| Internal tables | `_pgmi_parameter`, `_pgmi_runtime`, `_pgmi_source`, `_pgmi_source_metadata`, `_pgmi_test_directory`, `_pgmi_test_source` |
| Public views | `pgmi_source_view`, `pgmi_parameter_view`, `pgmi_runtime_view`, `pgmi_plan_view`, `pgmi_test_source_view`, `pgmi_test_directory_view`, `pgmi_source_metadata_view` |
| Functions | `pgmi_test_plan(p_pattern)`, `pgmi_test_generate(p_pattern, p_callback)`, `pgmi_register_file(...)`, `pgmi_test_callback(event)` |
| Preprocessor macro | `CALL pgmi_test()` — expanded by Go before SQL reaches PostgreSQL |
| Types | `pgmi_test_event` composite type for test lifecycle callbacks |

The execution contract — atomic head, then psql tail — is also pgmi core: the binary decides how to send your SQL to PostgreSQL. That behavior follows the binary version, not `--compat`.

### The basic template

A small two-layer starting project copied by `pgmi init`: `deploy.sql`, `physical/` (run-once tables), `semantic/` (re-run views and functions) and their `__test__/` directories. Yours the moment it lands.

### The advanced template

A complete reference application: handler registry, REST/RPC/MCP gateways, OpenAPI generation, multi-tenant RLS, transaction policy, and audit trails. Also copied by `pgmi init --template advanced`. Also yours; also deletable.

## The rule

**pgmi's job ends at preparing the session and executing your `deploy.sql`. Everything reachable from `deploy.sql` is yours.**

Nothing in either template is upgraded, migrated, or overwritten when you upgrade pgmi. Deleting half the advanced template is a supported outcome, not a downgrade.

## What happens on upgrade

**Core session API** — may change in a minor release before pgmi 1.0. A view, column or function can be added, renamed or removed; every such change is listed under **Upgrading** in that release's notes ([RELEASES.md](https://github.com/vvka-141/pgmi/blob/main/RELEASES.md)). `--compat` selects a contract version the binary ships (only `1` today) and rejects one it does not; it does not freeze columns across pgmi releases. Pin the pgmi binary version: that pins the session API and its behavior (plan ordering, which files load, the execution contract). Read Upgrading before you move the pin.

**Your project SQL** — untouched. pgmi never reads, diffs, or modifies your `deploy.sql` or any file it discovers. The binary loads files into temp tables and hands control to your SQL.

## Which docs apply to you

| You are... | Read these |
|------------|-----------|
| Any pgmi user | [Quickstart](QUICKSTART.md), [Session API](session-api.md), [deploy.sql guide](DEPLOY-GUIDE.md), [CLI reference](CLI.md), [Testing](TESTING.md) |
| Using the basic template or your own project shape | The above, plus [Script metadata](METADATA.md) when you need execution ordering |
| Using the advanced template | Everything above, plus [the advanced template section](advanced/README.md) — REST, MCP, transaction policy, client generation |

The [advanced template overview](advanced/README.md) states the boundary in full detail and documents every subsystem you inherit.
