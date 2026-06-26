---
title: pgmi
description: "pgmi is a PostgreSQL-native execution fabric: it loads your project files into session-scoped temp tables and hands control to your own deploy.sql. An execution fabric, not a migration framework — you write the deployment logic in SQL."
---

# pgmi

**A PostgreSQL-native execution fabric — not a migration framework.**

pgmi loads your project files and parameters into PostgreSQL session-scoped temp
tables, then hands control to your `deploy.sql`. Execution order, transactions,
idempotency, locking, rollback — you write them in SQL, not in tool
configuration. Most tools decide those things for you; pgmi gets out of the way.

> **Most tools** — your files → the tool decides order & transactions → database
>
> **pgmi** — your files → PostgreSQL temp tables → *your* `deploy.sql` decides → database

## Why it exists

Real deployments need environment-specific behavior — recreate everything in
dev, run only changed migrations in staging, require approval for destructive
changes in production. With most tools that means config files, framework
conditionals, and wrapper scripts. With pgmi, deployment policy stays in
`deploy.sql`, where the full power of PostgreSQL's procedural languages is
already there. The CLI handles infrastructure only — connections, parameters,
observability — and never deployment orchestration.

## Try in 60 seconds

```bash
# 1. Install (macOS / Linux — no Go toolchain needed)
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash

# 2. Scaffold a project
pgmi init myapp --template basic
cd myapp

# 3. Deploy to your local PostgreSQL
pgmi deploy . --overwrite --force
```

Windows PowerShell install: `irm https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.ps1 | iex`.
Full walkthrough in the [Quickstart]({{< relref "docs/quickstart" >}}).

## Choose your path

| | **basic** | **advanced** |
|---|---|---|
| Best for | A small, explicit migration scaffold | A full SQL-native reference app you own and trim |
| Ordering | Path-based (`001_`, `002_`) | Metadata-driven `<pgmi-meta>` sort keys |
| Includes | `migrations/`, `__test__/` | Schemas, roles, audit logging, REST/RPC/MCP APIs |
| MCP | None | Full MCP server for AI assistants |

Either can run in production — advanced is *more complete*, not *more
production*. See [Choosing a template]({{< relref "docs/quickstart" >}}#choosing-a-template).

## Go deeper

- [Why pgmi]({{< relref "docs/why-pgmi" >}}) — the approach, and when it does *not* fit
- [Documentation]({{< relref "docs" >}}) — full reference
- [GitHub](https://github.com/vvka-141/pgmi) — source, releases, issues
