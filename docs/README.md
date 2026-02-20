# pgmi Documentation

## Recommended Reading Order

**New to pgmi?** Start here:
1. [QUICKSTART.md](QUICKSTART.md) — Deploy your first project
2. [WHY-PGMI.md](WHY-PGMI.md) — Understand the philosophy
3. [session-api.md](session-api.md) — Learn the session API
4. [TRADEOFFS.md](TRADEOFFS.md) — Understand the honest costs

**Migrating from another tool?**
1. [COMING-FROM.md](COMING-FROM.md) — Flyway, Liquibase, psql migration guides
2. [QUICKSTART.md](QUICKSTART.md) — See pgmi in action

**Writing deploy.sql?**
1. [DEPLOY-GUIDE.md](DEPLOY-GUIDE.md) — Patterns cookbook (data ingestion, environment branching, multi-phase)
2. [session-api.md](session-api.md) — Views, columns, and functions reference

**Setting up production?**
1. [CONNECTIONS.md](CONNECTIONS.md) — Connection architecture (cloud auth, SSL, poolers)
2. [SECURITY.md](SECURITY.md) — Secrets and CI/CD patterns
3. [PRODUCTION.md](PRODUCTION.md) — Performance and rollback strategies
4. [CLI.md](CLI.md) — All flags and exit codes

**Adding tests?**
1. [TESTING.md](TESTING.md) — `CALL pgmi_test()` and fixtures

**Using the advanced template?**
1. [METADATA.md](METADATA.md) — Script tracking with `<pgmi-meta>`
2. [MCP.md](MCP.md) — AI assistant integration

---

## Quick Answers

| Question | Answer |
|----------|--------|
| Which view should I use? | `pgmi_plan_view` for deployment, `pgmi_source_view` for introspection — see [session-api.md](session-api.md#which-view-should-i-use) |
| How do I access CLI parameters? | `current_setting('pgmi.key', true)` — see [session-api.md](session-api.md#parameters) |
| How do I run tests? | `CALL pgmi_test()` in deploy.sql — see [TESTING.md](TESTING.md) |
| What's the difference between templates? | Basic = learning, Advanced = production — see [QUICKSTART.md](QUICKSTART.md#choosing-a-template) |
| How do I filter which files run? | `WHERE` clause on `pg_temp.pgmi_plan_view` — see [session-api.md](session-api.md) |
| What exit codes does pgmi use? | 0=success, 13=SQL error, etc. — see [CLI.md](CLI.md#exit-codes) |

---

## All Documentation

### Getting Started
- **[QUICKSTART.md](QUICKSTART.md)** — Your first deployment (install, configure, deploy, verify)
- **[WHY-PGMI.md](WHY-PGMI.md)** — When pgmi's approach makes sense (and when it doesn't)
- **[COMING-FROM.md](COMING-FROM.md)** — Migration guides from Flyway, Liquibase, and raw psql

### Reference
- **[CLI.md](CLI.md)** — Complete CLI reference (commands, flags, exit codes, error messages)
- **[CONFIGURATION.md](CONFIGURATION.md)** — pgmi.yaml schema and precedence rules
- **[session-api.md](session-api.md)** — Session views and functions (`pg_temp.pgmi_*`)

### Guides
- **[DEPLOY-GUIDE.md](DEPLOY-GUIDE.md)** — deploy.sql authoring patterns (data ingestion, environment branching, multi-phase)
- **[CONNECTIONS.md](CONNECTIONS.md)** — Connection architecture (cloud auth, SSL, poolers, IaC)
- **[TRADEOFFS.md](TRADEOFFS.md)** — Honest limitations and who should use pgmi

### Features
- **[TESTING.md](TESTING.md)** — Database testing with automatic rollback
- **[METADATA.md](METADATA.md)** — Script tracking with UUIDs, idempotency, sort keys
- **[SECURITY.md](SECURITY.md)** — Secrets handling and CI/CD patterns
- **[MCP.md](MCP.md)** — Model Context Protocol for AI assistants

### Operations
- **[PRODUCTION.md](PRODUCTION.md)** — Performance, rollback strategies, monitoring

### AI Integration
```bash
pgmi ai                    # Overview for AI assistants
pgmi ai skills             # List embedded skills
pgmi ai skill pgmi-sql     # Load SQL conventions
```
See [CLI.md#pgmi-ai](CLI.md#pgmi-ai) for details.
