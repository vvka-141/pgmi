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
- Same inputs = same outputs, always
- The deployment plan is queryable data, not opaque framework state
- Clear success/failure signals via PostgreSQL exceptions

## When pgmi is overkill

pgmi handles linear migrations out of the box (the basic template does exactly this). pgmi ships with two templates — **basic** for learning and simple projects, **advanced** for production with metadata-driven deployment. See [Choosing a Template](QUICKSTART.md#choosing-a-template) for details.

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
| Sqitch | Dependency graph in plan file | You implement dependencies in `deploy.sql` |

## Next steps

- [Getting Started](QUICKSTART.md) — Your first deployment in 10 minutes
- [Session API Reference](session-api.md) — The temp tables and functions available to `deploy.sql`
- [Coming from Other Tools](COMING-FROM.md) — Migration guides from Flyway, Liquibase, etc.
