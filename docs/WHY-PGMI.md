# Why pgmi?

pgmi takes a different approach to database deployments. This document explains when that approach makes sense—and when it doesn't.

## The core idea

Most deployment tools work like this:

```
Your files → Tool decides order → Tool decides transactions → Database
```

pgmi works like this:

```
Your files → PostgreSQL temp tables → YOUR deploy.sql decides everything → Database
```

The difference: **you write the deployment logic in SQL, not configuration.**

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
    v_env TEXT := pg_temp.pgmi_get_param('env', 'development');
    v_file RECORD;
BEGIN
    IF v_env = 'development' THEN
        -- Recreate everything
        PERFORM pg_temp.pgmi_plan_command('DROP SCHEMA IF EXISTS app CASCADE;');
        PERFORM pg_temp.pgmi_plan_command('CREATE SCHEMA app;');
    END IF;

    -- Always run migrations
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE path ~ '^./migrations' AND is_sql_file ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    IF v_env = 'production' THEN
        -- Log deployment for audit
        PERFORM pg_temp.pgmi_plan_command(
            format('INSERT INTO audit.deployments (deployed_at, env) VALUES (now(), %L);', v_env)
        );
    END IF;
END $$;
```

No framework DSL. No YAML conditionals. Just PostgreSQL.

> The `pgmi_plan_*` functions above don't run SQL immediately—they schedule commands for execution after `deploy.sql` finishes. This is what makes the `IF v_env` conditional work: you build completely different execution plans based on runtime conditions, and nothing touches the database until the plan is final. See [Session API](session-api.md).

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
- Same inputs = same outputs, always
- The deployment plan is queryable data, not opaque framework state
- Clear success/failure signals via PostgreSQL exceptions

## When pgmi is overkill

pgmi handles linear migrations out of the box (the basic template does exactly this). But consider simpler tools if:

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

## Design principles

### PostgreSQL is the deployment engine

pgmi doesn't implement migration logic in Go. It loads your files into PostgreSQL and lets PostgreSQL run your deployment script. This means:

- You can use any PostgreSQL feature: `pg_advisory_lock`, system catalogs, extensions
- Errors are PostgreSQL errors with standard SQLSTATE codes
- Debugging uses PostgreSQL tools you already know

### Infrastructure, not orchestration

pgmi provides:
- File loading into temp tables
- Parameter injection
- Plan execution

pgmi does NOT decide:
- Transaction boundaries (you write `BEGIN`/`COMMIT`)
- Execution order (you query and sort `pgmi_source`)
- Retry logic (you use `EXCEPTION` blocks)
- Idempotency (you write `IF NOT EXISTS`, `ON CONFLICT`)

### Your SQL remains portable

pgmi adds no annotations to your SQL files. A migration file used with pgmi is valid PostgreSQL SQL—you can run it directly with `psql` if needed.

The only pgmi-specific code is `deploy.sql`, which uses temp table functions. If you later switch tools, your migration files work unchanged.

## Comparison with other tools

For detailed migration guides, see [Coming from Other Tools](COMING-FROM.md).

| Tool | How it works | pgmi equivalent |
|------|--------------|-----------------|
| Flyway | Numbered files, framework runs in order | You query `pgmi_source`, sort as needed |
| Liquibase | Changelog XML/YAML, framework interprets | Your `deploy.sql` interprets |
| Raw psql scripts | Manual execution order | `deploy.sql` automates the ordering |
| Sqitch | Dependency graph in plan file | You implement dependencies in `deploy.sql` |

## Next steps

- [Getting Started](QUICKSTART.md) — Your first deployment in 10 minutes
- [Session API Reference](session-api.md) — The temp tables and functions available to `deploy.sql`
- [Coming from Other Tools](COMING-FROM.md) — Migration guides from Flyway, Liquibase, etc.
