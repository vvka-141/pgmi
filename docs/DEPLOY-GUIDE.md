# deploy.sql Authoring Guide

This guide covers patterns for writing `deploy.sql` — from basic file execution to data ingestion, environment branching, and multi-phase deployments. Every example is copy-paste ready.

For the session API reference (views, columns, functions), see [session-api.md](session-api.md).

---

## The basic pattern

Your `deploy.sql` queries files from session views and executes them with `EXECUTE`:

```sql
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

Use `pgmi_plan_view` instead of `pgmi_source_view` when you want metadata-driven ordering via `<pgmi-meta>` blocks. See [session-api.md](session-api.md#which-view-should-i-use).

---

## Environment branching

Use `current_setting('pgmi.env', true)` to branch deployment logic by environment:

```sql
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
    v_file RECORD;
BEGIN
    IF v_env = 'development' THEN
        EXECUTE 'DROP SCHEMA IF EXISTS app CASCADE';
        EXECUTE 'CREATE SCHEMA app';
    END IF;

    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    IF v_env = 'production' THEN
        INSERT INTO audit.deployments (deployed_at, env) VALUES (now(), v_env);
    END IF;
END $$;
```

```bash
pgmi deploy . -d myapp --param env=production
```

---

## Error context with exception blocks

Wrap each file execution in an exception block to see which file failed:

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
        END;
    END LOOP;
END $$;
```

The transaction still rolls back entirely on failure — the exception block is for diagnostics, not partial commits.

---

## Idempotent migrations with tracking

Track which files have run to avoid re-executing non-idempotent migrations:

```sql
CREATE TABLE IF NOT EXISTS migration_log (
    path TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    executed_at TIMESTAMPTZ DEFAULT now()
);

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content, checksum
        FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        IF EXISTS (
            SELECT 1 FROM migration_log
            WHERE path = v_file.path AND checksum = v_file.checksum
        ) THEN
            RAISE NOTICE 'Skipping (unchanged): %', v_file.path;
            CONTINUE;
        END IF;

        EXECUTE v_file.content;

        INSERT INTO migration_log (path, checksum)
        VALUES (v_file.path, v_file.checksum)
        ON CONFLICT (path) DO UPDATE
            SET checksum = EXCLUDED.checksum, executed_at = now();
    END LOOP;
END $$;
```

The `checksum` column from `pgmi_source_view` is a SHA-256 of the original file content. If the file changes, it re-executes.

---

## Loading JSON configuration

pgmi loads **all** project files — not just SQL. Use `pgmi_source_view` to read JSON, XML, CSV, or any text file.

Given `./config/app.json`:
```json
{
  "feature_flags": {
    "dark_mode": true,
    "beta_features": false
  },
  "rate_limits": {
    "api_requests_per_minute": 100
  }
}
```

```sql
DO $$
DECLARE
    v_config JSONB;
    v_file RECORD;
BEGIN
    CREATE TABLE IF NOT EXISTS app_config (
        key TEXT PRIMARY KEY,
        value JSONB NOT NULL,
        updated_at TIMESTAMPTZ DEFAULT now()
    );

    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_source_view
        WHERE path = './config/app.json'
    ) LOOP
        v_config := v_file.content::jsonb;

        INSERT INTO app_config (key, value)
        SELECT key, value FROM jsonb_each(v_config)
        ON CONFLICT (key) DO UPDATE
            SET value = EXCLUDED.value, updated_at = now();
    END LOOP;
END $$;
```

### Loading environment-specific config

```sql
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
    v_file RECORD;
    v_config JSONB;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_source_view
        WHERE path = './config/' || v_env || '.json'
    ) LOOP
        v_config := v_file.content::jsonb;

        INSERT INTO app_config (key, value, environment)
        SELECT key, value, v_env FROM jsonb_each(v_config)
        ON CONFLICT (key, environment) DO UPDATE
            SET value = EXCLUDED.value, updated_at = now();
    END LOOP;
END $$;
```

---

## Loading XML reference data

Given `./data/currencies.xml`:
```xml
<currencies>
    <currency code="USD" name="US Dollar" symbol="$" decimals="2"/>
    <currency code="EUR" name="Euro" symbol="€" decimals="2"/>
    <currency code="JPY" name="Japanese Yen" symbol="¥" decimals="0"/>
</currencies>
```

```sql
DO $$
DECLARE
    v_xml XML;
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_source_view
        WHERE path = './data/currencies.xml'
    ) LOOP
        v_xml := v_file.content::xml;

        INSERT INTO currency (code, name, symbol, decimal_places)
        SELECT
            (xpath('@code', x))[1]::text,
            (xpath('@name', x))[1]::text,
            (xpath('@symbol', x))[1]::text,
            ((xpath('@decimals', x))[1]::text)::int
        FROM unnest(xpath('/currencies/currency', v_xml)) AS x
        ON CONFLICT (code) DO UPDATE
            SET name = EXCLUDED.name,
                symbol = EXCLUDED.symbol,
                decimal_places = EXCLUDED.decimal_places;
    END LOOP;
END $$;
```

---

## Loading CSV data

For simple CSV files without quoting or escaping:

```sql
DO $$
DECLARE
    v_file RECORD;
    v_lines TEXT[];
    v_line TEXT;
    v_fields TEXT[];
    v_row_num INT := 0;
BEGIN
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_source_view
        WHERE path = './data/countries.csv'
    ) LOOP
        v_lines := string_to_array(v_file.content, E'\n');

        FOREACH v_line IN ARRAY v_lines LOOP
            v_row_num := v_row_num + 1;
            IF v_row_num = 1 THEN CONTINUE; END IF;
            IF v_line = '' THEN CONTINUE; END IF;

            v_fields := string_to_array(v_line, ',');

            INSERT INTO country (code, name)
            VALUES (v_fields[1], v_fields[2])
            ON CONFLICT DO NOTHING;
        END LOOP;
    END LOOP;
END $$;
```

For large or complex CSV files (quoted fields, escaping), use PostgreSQL's `COPY` command with an external file instead. PL/pgSQL string splitting is adequate for reference data (hundreds to low thousands of rows), not bulk imports.

---

## Checksum-based change detection

Skip files that haven't changed since the last deployment:

```sql
CREATE TABLE IF NOT EXISTS loaded_data_file (
    path TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    loaded_at TIMESTAMPTZ DEFAULT now()
);

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content, checksum
        FROM pg_temp.pgmi_source_view
        WHERE directory = './data/' AND extension = '.json'
    ) LOOP
        IF EXISTS (
            SELECT 1 FROM loaded_data_file
            WHERE path = v_file.path AND checksum = v_file.checksum
        ) THEN
            RAISE NOTICE 'Skipping (unchanged): %', v_file.path;
            CONTINUE;
        END IF;

        -- Process file content here

        INSERT INTO loaded_data_file (path, checksum)
        VALUES (v_file.path, v_file.checksum)
        ON CONFLICT (path) DO UPDATE
            SET checksum = EXCLUDED.checksum, loaded_at = now();
    END LOOP;
END $$;
```

---

## Multi-phase deployment

Separate transactional and non-transactional work into distinct phases:

```sql
-- Phase 1: Schema changes (transactional)
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;

-- Phase 2: Non-transactional operations
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './post-deploy/' AND is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Post-deploy: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

---

## CREATE INDEX CONCURRENTLY workaround

`CREATE INDEX CONCURRENTLY` cannot run inside a transaction block. Structure your deploy.sql to handle this:

```sql
-- Phase 1: Schema changes (transactional)
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
            AND path NOT LIKE '%_concurrent.sql'
        ORDER BY path
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;

-- Phase 2: Concurrent indexes (non-transactional, must be idempotent)
-- These MUST be top-level statements — DO blocks create an implicit transaction
-- context, which causes CREATE INDEX CONCURRENTLY to fail.
-- pgmi's temp tables survive COMMIT (session-scoped), so they're still queryable.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_email ON users(email);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_order_date ON orders(created_at);
```

Because top-level SQL has no procedural constructs (no loops, no variables), concurrent index statements must be written explicitly — you cannot dynamically iterate `pgmi_source_view` for them. Always use `IF NOT EXISTS`:

```sql
-- migrations/003_user_email_concurrent.sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_email ON users(email);
```

This is a PostgreSQL constraint, not a pgmi limitation — `CREATE INDEX CONCURRENTLY` behaves the same way in Flyway, Liquibase, and every other tool that uses transactions. See [TRADEOFFS.md](TRADEOFFS.md#create-index-concurrently) for more context.

---

## Gated deployment with test gate

Run tests between deployment phases. If any test fails, the entire transaction rolls back:

```sql
BEGIN;

DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;

-- Tests run inside the transaction — failure aborts everything
CALL pgmi_test();

COMMIT;
```

See [TESTING.md](TESTING.md#the-gated-deployment-pattern) for details on how the test gate works.

---

## Flavor-specific deployment

Deploy to PostgreSQL flavors (Citus, TimescaleDB, PostGIS) with the same `deploy.sql`:

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;

    -- Citus: distribute tables (only on Citus instances)
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'citus') THEN
        PERFORM create_distributed_table('tenant_data', 'tenant_id');
        PERFORM create_reference_table('plan_tier');
        RAISE NOTICE 'Citus distribution configured';
    END IF;

    -- TimescaleDB: create hypertables
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'timescaledb') THEN
        PERFORM create_hypertable('sensor_reading', 'recorded_at',
            chunk_time_interval => interval '1 day',
            if_not_exists => true
        );
        PERFORM add_compression_policy('sensor_reading', interval '7 days',
            if_not_exists => true
        );
        RAISE NOTICE 'TimescaleDB hypertables configured';
    END IF;

    -- PostGIS: create spatial indexes
    IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'postgis') THEN
        EXECUTE 'CREATE INDEX IF NOT EXISTS idx_location_geom ON location USING gist(geom)';
        RAISE NOTICE 'PostGIS spatial indexes configured';
    END IF;
END $$;
```

The `IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = '...')` pattern lets the same `deploy.sql` work on vanilla PostgreSQL and flavored instances. pgmi handles the connection; PostgreSQL handles the flavor; your SQL handles the logic.

See [CONNECTIONS.md](CONNECTIONS.md#where-pgmi-doesnt-work) for compatibility notes on non-PostgreSQL databases.

---

## Complete multi-environment example

A production-ready `deploy.sql` combining environment branching, data ingestion, checksum tracking, and test gating:

```sql
BEGIN;

DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
    v_file RECORD;
BEGIN
    -- Schema migrations
    RAISE NOTICE '=== Applying migrations ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_source_view
        WHERE directory = './migrations/' AND is_sql_file
        ORDER BY path
    ) LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
        END;
    END LOOP;

    -- Load environment-specific config
    RAISE NOTICE '=== Loading config for: % ===', v_env;
    FOR v_file IN (
        SELECT content FROM pg_temp.pgmi_source_view
        WHERE path = './config/' || v_env || '.json'
    ) LOOP
        INSERT INTO app_config (key, value, environment)
        SELECT key, value, v_env FROM jsonb_each(v_file.content::jsonb)
        ON CONFLICT (key, environment) DO UPDATE
            SET value = EXCLUDED.value, updated_at = now();
    END LOOP;

    -- Load reference data (idempotent via checksum)
    RAISE NOTICE '=== Loading reference data ===';
    FOR v_file IN (
        SELECT path, content, checksum FROM pg_temp.pgmi_source_view
        WHERE directory = './data/' AND extension = '.json'
        ORDER BY path
    ) LOOP
        IF NOT EXISTS (
            SELECT 1 FROM data_load_history WHERE checksum = v_file.checksum
        ) THEN
            INSERT INTO reference_data (key, value)
            SELECT key, value FROM jsonb_each(v_file.content::jsonb)
            ON CONFLICT DO NOTHING;

            INSERT INTO data_load_history (path, checksum)
            VALUES (v_file.path, v_file.checksum);
        END IF;
    END LOOP;

    -- Development-only seed data
    IF v_env = 'development' THEN
        RAISE NOTICE '=== Loading dev seed data ===';
        FOR v_file IN (
            SELECT content FROM pg_temp.pgmi_source_view
            WHERE directory = './seeds/dev/' AND extension = '.json'
            ORDER BY path
        ) LOOP
            INSERT INTO users (email, name, role)
            SELECT u->>'email', u->>'name', u->>'role'
            FROM jsonb_array_elements(v_file.content::jsonb) AS u
            ON CONFLICT (email) DO NOTHING;
        END LOOP;
    END IF;
END $$;

-- Run tests (savepoint ensures test side effects roll back)
CALL pgmi_test();

COMMIT;
```

```bash
# Development
pgmi deploy . -d myapp_dev --overwrite --force --param env=development

# Production
pgmi deploy . -d myapp --param env=production --force
```

---

## See also

- [Session API Reference](session-api.md) — Views, columns, and functions available in deploy.sql
- [Testing Guide](TESTING.md) — Savepoint isolation and the gated deployment pattern
- [Production Guide](PRODUCTION.md) — Deployment strategies, locks, monitoring
- [Metadata Guide](METADATA.md) — `<pgmi-meta>` blocks for execution ordering
- [Tradeoffs](TRADEOFFS.md) — Honest limitations of pgmi's approach
