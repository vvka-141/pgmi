---
title: "Why pgmi"
description: "Understand when pgmi's SQL-driven execution fabric fits a PostgreSQL project and when another migration tool is a better choice."
weight: 20
---

# Why pgmi?

pgmi takes a different approach to database deployments. This document explains when that approach makes sense—and when it doesn't.

## The core idea

Most deployment tools take your files, decide the order and the transaction boundaries themselves, and apply the result. pgmi loads your files into PostgreSQL temp tables and lets **your deploy.sql** decide everything on the way to the database.

The difference: **you write the deployment logic in SQL, not configuration.**

![Migration framework vs pgmi execution fabric: the tool decides vs your deploy.sql decides](diagrams/d02-fabric-vs-framework.drawio.svg)

## A concrete example

Suppose you need environment-specific deployment behavior:
- In development: drop and recreate everything
- In staging: run migrations only if checksums changed
- In production: require explicit approval for destructive changes

**With traditional tools**, you'd need:
- Multiple configuration files
- Framework-specific conditionals
- External scripts wrapping the tool

**With pgmi**, it's just SQL:

```sql
-- deploy.sql
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
    v_file RECORD;
BEGIN
    IF v_env = 'development' THEN
        -- Recreate everything
        EXECUTE 'DROP SCHEMA IF EXISTS app CASCADE';
        EXECUTE 'CREATE SCHEMA app';
    END IF;

    -- Always run migrations
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    IF v_env = 'production' THEN
        -- Log deployment for audit
        INSERT INTO audit.deployments (deployed_at, env) VALUES (now(), v_env);
    END IF;
END $$;
```

No framework DSL. No YAML conditionals. Just PostgreSQL.

> Your `deploy.sql` queries `pg_temp.pgmi_plan_view` (or `pg_temp.pgmi_source_view`) and uses `EXECUTE` to run files directly. The `IF v_env` conditional controls what SQL runs based on runtime conditions. See [Session API](session-api.md).

## When pgmi makes sense

pgmi is a good fit when:

**You need deployment logic beyond linear migrations.**
- Conditional execution based on environment
- Custom ordering that isn't alphabetical
- Multi-phase deployments with different transaction strategies

**You want explicit transaction control.**
- Some migrations need individual transactions (to allow partial progress)
- Some need a single transaction (all-or-nothing)
- Traditional tools make you choose one approach globally

**Your team is fluent in SQL/PL/pgSQL.**
- pgmi's power comes from writing deployment logic in PostgreSQL's native language
- If your team avoids SQL, pgmi's advantage disappears

**You're building automation that needs predictability.**
- pgmi itself adds no hidden state: the session is built only from your files and parameters (what your SQL then reads — clocks, catalogs, live data — is your choice)
- The deployment plan is queryable data, not opaque framework state
- Clear success/failure signals via PostgreSQL exceptions and documented exit codes

**You deploy data files alongside schema.**
- JSON configuration, XML reference data, CSV seed data — loaded and processed in the same transaction as your migrations
- Checksum columns in `pgmi_source_view` enable change detection — your deploy.sql can compare checksums against a tracking table to skip unchanged files (the advanced template does this automatically)
- See [deploy.sql guide](DEPLOY-GUIDE.md#loading-json-configuration) for data ingestion patterns

**You target multiple cloud PostgreSQL providers.**
- Same `deploy.sql` works on Azure Database for PostgreSQL, Amazon RDS, Google Cloud SQL, Citus, TimescaleDB, Neon, Supabase
- Native auth integration (Azure Entra ID, AWS IAM, Google Cloud SQL IAM) — no credential translation layer
- See [Connections](CONNECTIONS.md) for the full connection architecture

**You want fast iteration with disposable databases.**
- `pgmi deploy . --overwrite --force` drops and recreates the database, then deploys from scratch
- Tests run inside the deployment transaction and roll back automatically
- Zero manual cleanup between iterations

## When pgmi is overkill

pgmi handles linear migrations out of the box (the basic template does exactly this). pgmi ships with two templates — **basic**, a small, explicit migration scaffold, and **advanced**, a large, editable reference system (metadata tracking, roles, RLS, API/MCP). Advanced is _more complete_, not _more production_ — either can be production depending on your project. See [Choosing a Template](QUICKSTART.md#choosing-a-template) for the canonical comparison.

But consider simpler tools if:

**You'll never need anything beyond linear migrations.**
If you're certain your deployments will always be "run these numbered files in order" with no conditionals, no custom transaction strategies, and no testing gates — Flyway or Liquibase have a shallower learning curve.

**You prefer framework-managed complexity.**
Some teams prefer "the tool handles transactions" over "I control transactions." That's valid—pgmi requires you to think about transaction boundaries.

**You need a large ecosystem.**
Flyway and Liquibase have GUI tools, IDE plugins, extensive third-party integrations. pgmi is CLI-focused.

**You're not on PostgreSQL.**
pgmi is PostgreSQL-only by design. It leverages PostgreSQL-specific features (temp tables, PL/pgSQL, savepoints). Multi-database support is not planned.

## The tradeoffs

| Aspect | pgmi | Traditional tools |
|--------|------|-------------------|
| Learning curve | Low to start (templates work out of the box), higher for custom logic | Lower (conventions handle it) |
| Flexibility | Maximum (full PL/pgSQL) | Limited (framework DSL) |
| Debugging | PostgreSQL-native (RAISE, pg_catalog) | Tool-specific logs |
| Portability | PostgreSQL only | Often multi-database |
| Transaction control | Explicit (you decide) | Implicit (framework decides) |
| Data ingestion | Built-in (JSON, XML, CSV via deploy.sql) | External tools or plugins |
| Cloud auth | Native (Azure, AWS, GCP IAM) | Varies by tool |
| File loading | Session temp tables (disk-backed), suited for schema + reference data | Varies |
| Connection poolers | Direct connection required | Usually transparent |

For a deeper exploration of pgmi's costs, see [Trade-offs](TRADEOFFS.md).

## Design principles

### PostgreSQL is the deployment engine

pgmi doesn't implement migration logic in Go. It loads your files into PostgreSQL and lets PostgreSQL run your deployment script. This means:

- You can use any PostgreSQL feature: `pg_advisory_lock`, system catalogs, extensions
- Errors are PostgreSQL errors with standard SQLSTATE codes
- Debugging uses PostgreSQL tools you already know

### Infrastructure, not orchestration

pgmi provides:
- File discovery and loading into temp tables
- Parameter injection as session variables
- Optional metadata parsing (`<pgmi-meta>`) for execution ordering
- Preprocessor macro expansion (`CALL pgmi_test()`)
- deploy.sql execution

pgmi does NOT decide:
- Transaction boundaries (you write `BEGIN`/`COMMIT`)
- Which files run (you query and filter `pgmi_plan_view` or `pgmi_source_view`)
- Retry logic (you use `EXCEPTION` blocks)
- Idempotency (you write `IF NOT EXISTS`, `ON CONFLICT`)

### Your SQL remains portable

Your migration files are valid PostgreSQL SQL—you can run them directly with `psql`. The optional `<pgmi-meta>` blocks live inside standard SQL comments (`/* ... */`), so they don't break compatibility. pgmi parses them before SQL reaches PostgreSQL to configure file ordering and idempotency tracking — they're metadata about your files, not executable syntax.

The pgmi-specific parts are `deploy.sql` (which queries session temp tables) and any `<pgmi-meta>` blocks you choose to add. If you later switch tools, your migration files work unchanged — strip the comment blocks and they're plain SQL.

## Comparison with other tools

For detailed migration guides, see [Coming from Other Tools](COMING-FROM.md).

| Tool | How it works | pgmi equivalent |
|------|--------------|-----------------|
| Flyway | Numbered files, framework runs in order | You query `pg_temp.pgmi_source_view`, sort as needed |
| Liquibase | Changelog XML/YAML, framework interprets | Your `deploy.sql` interprets |
| Raw psql scripts | Manual execution order | `deploy.sql` automates the ordering |
| Sqitch | Changes, dependencies, verify, and revert are first-class tool concepts (native SQL scripts, no DSL) | The whole orchestration program is your SQL; those concepts exist only if you write them |
| Atlas | Declarative schema-as-code: the tool diffs desired vs actual state and plans migrations | Imperative and explicit: your `deploy.sql` states what runs; nothing is inferred |

Sqitch deserves a precise comparison because it shares pgmi's "plain SQL, no DSL" stance: Sqitch provides a mature change-management *model* (deploy/verify/revert scripts, dependency resolution, history). pgmi provides a smaller *mechanism* — your project as session data — with no prescribed semantics on top. If you want the tool to manage change state, Sqitch does that well; if you want to program the deployment yourself, that's pgmi. See [Coming from Sqitch](COMING-FROM.md#coming-from-sqitch).

## Next steps

- [Getting Started](QUICKSTART.md) — Your first deployment in 10 minutes
- [Session API Reference](session-api.md) — The temp tables and functions available to `deploy.sql`
- [Coming from Other Tools](COMING-FROM.md) — Migration guides from Flyway, Liquibase, etc.
