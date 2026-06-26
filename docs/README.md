---
title: "Overview"
description: "Find the right pgmi guide for installation, deployment, session APIs, testing, security, and production operation."
weight: 10
---

# pgmi Documentation

pgmi is a PostgreSQL-native deployment tool that loads your project files into session temp tables and lets your `deploy.sql` drive everything — transactions, execution order, and logic. These docs cover the session API, CLI, deployment patterns, testing, security, and operational guides.

## Recommended Reading Order

**New to pgmi?** Start here:
1. [Quickstart](QUICKSTART.md) — Deploy your first project
2. [Why pgmi](WHY-PGMI.md) — Understand the philosophy
3. [Session API](session-api.md) — Learn the session API
4. [Trade-offs](TRADEOFFS.md) — Understand the honest costs

**Migrating from another tool?**
1. [Coming from other tools](COMING-FROM.md) — Flyway, Liquibase, psql migration guides
2. [Quickstart](QUICKSTART.md) — See pgmi in action

**Writing deploy.sql?**
1. [deploy.sql guide](DEPLOY-GUIDE.md) — Patterns cookbook (data ingestion, environment branching, multi-phase)
2. [Session API](session-api.md) — Views, columns, and functions reference

**Setting up production?**
1. [Connections](CONNECTIONS.md) — Connection architecture (cloud auth, SSL, poolers)
2. [Security](SECURITY.md) — Secrets and CI/CD patterns
3. [CI/CD](CICD.md) — Deploy from GitHub Actions and other pipelines
4. [Production](PRODUCTION.md) — Performance and rollback strategies
5. [CLI reference](CLI.md) — All flags and exit codes

**Adding tests?**
1. [Testing](TESTING.md) — `CALL pgmi_test()` and fixtures

**Using the advanced template?**
1. [Script metadata](METADATA.md) — Script tracking with `<pgmi-meta>`
2. [MCP integration](MCP.md) — AI assistant integration

---

## Quick Answers

| Question | Answer |
|----------|--------|
| Which view should I use? | `pgmi_plan_view` for deployment, `pgmi_source_view` for introspection — see [Session API](session-api.md#which-view-should-i-use) |
| How do I access CLI parameters? | `current_setting('pgmi.key', true)` — see [Session API](session-api.md#parameters) |
| How do I run tests? | `CALL pgmi_test()` in deploy.sql — see [Testing](TESTING.md) |
| What's the difference between templates? | Basic = small migration scaffold, Advanced = richer reference app — both production-capable; see [Quickstart](QUICKSTART.md#choosing-a-template) |
| How do I filter which files run? | `WHERE` clause on `pg_temp.pgmi_plan_view` — see [Session API](session-api.md) |
| What exit codes does pgmi use? | 0=success, 13=SQL error, etc. — see [CLI reference](CLI.md#exit-codes) |

---

## All Documentation

### Getting Started
- **[Quickstart](QUICKSTART.md)** — Your first deployment (install, configure, deploy, verify)
- **[Why pgmi](WHY-PGMI.md)** — When pgmi's approach makes sense (and when it doesn't)
- **[Coming from other tools](COMING-FROM.md)** — Migration guides from Flyway, Liquibase, and raw psql

### Reference
- **[CLI reference](CLI.md)** — Complete CLI reference (commands, flags, exit codes, error messages)
- **[Configuration](CONFIGURATION.md)** — pgmi.yaml schema and precedence rules
- **[Session API](session-api.md)** — Session views and functions (`pg_temp.pgmi_*`)

### Guides
- **[deploy.sql guide](DEPLOY-GUIDE.md)** — deploy.sql authoring patterns (data ingestion, environment branching, multi-phase)
- **[Connections](CONNECTIONS.md)** — Connection architecture (cloud auth, SSL, poolers, IaC)
- **[Trade-offs](TRADEOFFS.md)** — Honest limitations and who should use pgmi

### Features
- **[Testing](TESTING.md)** — Database testing with automatic rollback
- **[Script metadata](METADATA.md)** — Script tracking with UUIDs, idempotency, sort keys
- **[Security](SECURITY.md)** — Secrets handling and CI/CD patterns
- **[MCP integration](MCP.md)** — Model Context Protocol for AI assistants

### Operations
- **[CI/CD](CICD.md)** — Deploy from GitHub Actions and other pipelines
- **[Production](PRODUCTION.md)** — Performance, rollback strategies, monitoring

### Recipes (advanced, opt-in)
- **[Semantic MCP curation](recipes/semantic-mcp-tool-curation.md)** — Surface the relevant subset of agent tools by embedding similarity (provider-abstracted; for tool-overload scale)

### AI Integration
```bash
pgmi ai                    # Overview for AI assistants
pgmi ai skills             # List embedded skills
pgmi ai skill pgmi-sql     # Load SQL conventions
```
See [CLI.md#pgmi-ai](CLI.md#pgmi-ai) for details.
