# Production Guide

This guide covers considerations for running pgmi in production environments: performance, rollback strategies, monitoring, and operational patterns.

## Deployment strategies

### Single-transaction deployment

All changes succeed or fail together. Maximum safety, but holds locks longer.

```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
```

**When to use:**
- Small deployments (< 10 files)
- All changes are quick (no long-running DDL)
- You need all-or-nothing semantics

**Tradeoffs:**
- Locks held until all migrations complete
- Long-running migrations block other operations
- Failure in any file rolls back everything

### Per-migration transactions

Each migration commits independently. Allows partial progress, but no atomic rollback.

```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_command('BEGIN;');
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        PERFORM pg_temp.pgmi_plan_command('COMMIT;');
    END LOOP;
END $$;
```

**When to use:**
- Large deployments with many files
- Migrations that can run independently
- You can tolerate partial completion

**Tradeoffs:**
- Failure leaves database in intermediate state
- You need compensating logic for rollback
- Easier to debug (smaller transaction scope)

### Phased deployment

Different transaction strategies for different phases.

```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Phase 1: Schema extensions (one transaction)
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './extensions' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    -- Phase 2: Migrations (per-file transactions for partial progress)
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './migrations' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_command('BEGIN;');
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        PERFORM pg_temp.pgmi_plan_command('COMMIT;');
    END LOOP;

    -- Phase 3: Idempotent setup (no transaction wrapper)
    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './setup' ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
    END LOOP;
END $$;
```

**When to use:**
- Production deployments with mixed requirements
- Extensions and DDL that need different handling
- Clear separation between migration types

## Lock management

### Understanding PostgreSQL locks

DDL operations acquire locks that can block reads and writes:

| Operation | Lock type | Blocks |
|-----------|-----------|--------|
| `CREATE TABLE` | AccessExclusiveLock | Everything on new table |
| `ALTER TABLE ADD COLUMN` | AccessExclusiveLock | All operations on table |
| `CREATE INDEX` | ShareLock | Writes (not reads) |
| `CREATE INDEX CONCURRENTLY` | ShareUpdateExclusiveLock | Other DDL only |

### Lock timeout strategy

Set aggressive lock timeouts to fail fast rather than queue indefinitely:

```sql
-- In your migration files
SET lock_timeout = '5s';  -- Fail if lock not acquired in 5 seconds

ALTER TABLE users ADD COLUMN phone TEXT;

RESET lock_timeout;
```

Or globally in deploy.sql:

```sql
PERFORM pg_temp.pgmi_plan_command('SET lock_timeout = ''10s'';');
-- ... migrations ...
PERFORM pg_temp.pgmi_plan_command('RESET lock_timeout;');
```

### Concurrent index creation

For large tables, use `CONCURRENTLY` to avoid blocking:

```sql
-- 003_add_user_email_index.sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);
```

**Note:** `CREATE INDEX CONCURRENTLY` cannot run inside a transaction. Plan accordingly:

```sql
-- deploy.sql: Handle concurrent index separately
PERFORM pg_temp.pgmi_plan_command('COMMIT;');  -- End current transaction
PERFORM pg_temp.pgmi_plan_file('./migrations/003_add_user_email_index.sql');  -- Runs outside transaction
PERFORM pg_temp.pgmi_plan_command('BEGIN;');  -- Start new transaction for remaining work
```

## Rollback strategies

### Automatic rollback (transaction-based)

If you use single-transaction deployment, PostgreSQL rolls back automatically on any error:

```sql
-- deploy.sql
PERFORM pg_temp.pgmi_plan_command('BEGIN;');
-- ... all migrations ...
PERFORM pg_temp.pgmi_plan_command('COMMIT;');
-- If any migration fails, nothing is committed
```

### Compensating transactions

For per-migration deployments, you need explicit rollback logic:

```sql
-- migrations/002_add_email_column.sql
ALTER TABLE users ADD COLUMN email TEXT;

-- rollback/002_add_email_column.sql
ALTER TABLE users DROP COLUMN IF EXISTS email;
```

Then in deploy.sql, implement rollback capability:

```sql
-- deploy.sql with rollback support
DO $$
DECLARE
    v_rollback BOOLEAN := pg_temp.pgmi_get_param('rollback', 'false')::boolean;
    v_file RECORD;
BEGIN
    IF v_rollback THEN
        -- Execute rollback scripts in reverse order
        FOR v_file IN (
            SELECT path FROM pg_temp.pgmi_source
            WHERE directory = './rollback'
            ORDER BY path DESC
        )
        LOOP
            PERFORM pg_temp.pgmi_plan_file(v_file.path);
        END LOOP;
    ELSE
        -- Normal deployment
        FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE directory = './migrations' ORDER BY path)
        LOOP
            PERFORM pg_temp.pgmi_plan_file(v_file.path);
        END LOOP;
    END IF;
END $$;
```

Usage:
```bash
pgmi deploy . -d mydb --param rollback=true
```

### Savepoint-based partial rollback

For fine-grained control within a transaction:

```sql
-- deploy.sql with savepoints
DO $$
DECLARE
    v_file RECORD;
BEGIN
    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_command(format('SAVEPOINT migration_%s;', md5(v_file.path)));
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        -- If this file fails, you can ROLLBACK TO SAVEPOINT migration_xxx
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');
END $$;
```

## Monitoring and observability

### Deployment progress

pgmi outputs PostgreSQL `RAISE NOTICE` messages. Capture them in your CI/CD:

```bash
pgmi deploy . -d mydb 2>&1 | tee deployment.log
```

### Custom progress tracking

Add notices in deploy.sql:

```sql
FOR v_file IN (SELECT path FROM pg_temp.pgmi_source ORDER BY path)
LOOP
    PERFORM pg_temp.pgmi_plan_notice('[%s/%s] Executing: %s',
        row_number() OVER (),
        (SELECT count(*) FROM pg_temp.pgmi_source),
        v_file.path
    );
    PERFORM pg_temp.pgmi_plan_file(v_file.path);
END LOOP;
```

### Audit logging

Log deployments to a table for historical tracking:

```sql
-- deploy.sql: Audit logging
DO $$
DECLARE
    v_deployment_id UUID := gen_random_uuid();
    v_file RECORD;
BEGIN
    -- Record deployment start
    INSERT INTO audit.deployments (id, started_at, env, files_count)
    VALUES (
        v_deployment_id,
        now(),
        pg_temp.pgmi_get_param('env', 'unknown'),
        (SELECT count(*) FROM pg_temp.pgmi_source WHERE is_sql_file)
    );

    PERFORM pg_temp.pgmi_plan_command('BEGIN;');

    FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path)
    LOOP
        PERFORM pg_temp.pgmi_plan_file(v_file.path);

        -- Log each file execution
        INSERT INTO audit.deployment_files (deployment_id, file_path, executed_at)
        VALUES (v_deployment_id, v_file.path, now());
    END LOOP;

    PERFORM pg_temp.pgmi_plan_command('COMMIT;');

    -- Record deployment completion
    UPDATE audit.deployments SET completed_at = now() WHERE id = v_deployment_id;
END $$;
```

## Performance considerations

### Timeout configuration

Set appropriate timeouts for your deployment size:

```bash
# Small deployments (default 3 minutes)
pgmi deploy . -d mydb

# Large deployments
pgmi deploy . -d mydb --timeout 30m

# Via pgmi.yaml
# timeout: 30m
```

### Statement timeout

For individual long-running statements, use PostgreSQL's `statement_timeout`:

```sql
-- In migration file
SET statement_timeout = '10min';
-- Long-running operation
CREATE INDEX idx_large_table ON large_table(column);
RESET statement_timeout;
```

### Connection pooling

pgmi uses a single connection for the entire deployment. If you use connection pooling (PgBouncer, etc.):

- Use **session mode** for deployments (transaction mode doesn't support temp tables)
- Consider a direct connection for deployments, bypassing the pooler

```bash
# Direct connection for deployment
pgmi deploy . --connection "postgresql://user:pass@db-server:5432/mydb"

# Application traffic goes through pooler
# postgresql://user:pass@pgbouncer:6432/mydb
```

## CI/CD patterns

### GitHub Actions

```yaml
deploy:
  runs-on: ubuntu-latest
  steps:
    - uses: actions/checkout@v4

    - name: Install pgmi
      run: |
        curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash

    - name: Deploy
      env:
        PGMI_CONNECTION_STRING: ${{ secrets.DATABASE_URL }}
      run: |
        pgmi deploy . -d ${{ vars.DATABASE_NAME }} \
          --param env=production \
          --timeout 15m
```

### GitLab CI

```yaml
deploy:
  stage: deploy
  image: golang:1.22
  before_script:
    - go install github.com/vvka-141/pgmi/cmd/pgmi@latest
  script:
    - pgmi deploy . -d $DATABASE_NAME --param env=production
  variables:
    PGMI_CONNECTION_STRING: $DATABASE_URL
```

### Deployment gates

Use pgmi's exit codes for pipeline control:

```bash
#!/bin/bash
pgmi deploy . -d mydb
exit_code=$?

case $exit_code in
    0)  echo "Deployment successful" ;;
    10) echo "Configuration error"; exit 1 ;;
    11) echo "Connection failed"; exit 1 ;;
    13) echo "SQL execution failed"; exit 1 ;;
    *)  echo "Unexpected error: $exit_code"; exit 1 ;;
esac
```

See [CLI Reference](CLI.md) for all exit codes.

## Multi-database deployments

### Sequential deployment

Deploy to multiple databases in sequence:

```bash
for db in db1 db2 db3; do
    echo "Deploying to $db..."
    pgmi deploy . -d $db --param env=production || exit 1
done
```

### Parallel deployment (with caution)

```bash
pgmi deploy . -d db1 --param env=production &
pgmi deploy . -d db2 --param env=production &
pgmi deploy . -d db3 --param env=production &
wait
```

**Warning:** Parallel deployment requires that migrations don't depend on cross-database state.

## Disaster recovery

### Pre-deployment backup

Always backup before production deployments:

```bash
pg_dump -Fc mydb > backup_$(date +%Y%m%d_%H%M%S).dump
pgmi deploy . -d mydb
```

### Point-in-time recovery

If using PostgreSQL's WAL archiving, note the LSN before deployment:

```sql
SELECT pg_current_wal_lsn();
-- Deploy
-- If rollback needed, restore to this LSN
```

### Blue-green deployments

Deploy to a standby database, then switch:

```bash
# 1. Deploy to blue (standby)
pgmi deploy . -d mydb_blue --param env=production

# 2. Run smoke tests against blue
pgmi test . -d mydb_blue

# 3. Switch traffic (application config or DNS)

# 4. Blue becomes production, green becomes standby
```

## Next steps

- [Security Guide](SECURITY.md) — Secrets handling in CI/CD
- [Testing Guide](TESTING.md) — Pre-deployment testing patterns
- [Retry and Timeout Behavior](retry-timeout-behavior.md) — Detailed timeout mechanics
