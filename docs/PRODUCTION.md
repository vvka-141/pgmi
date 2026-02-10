# Production Guide

This guide covers considerations for running pgmi in production environments: performance, rollback strategies, monitoring, and operational patterns.

## Deployment strategies

### Single-transaction deployment

All changes succeed or fail together. Maximum safety, but holds locks longer.

```sql
-- deploy.sql
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
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
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        BEGIN
            EXECUTE v_file.content;
        EXCEPTION WHEN OTHERS THEN
            RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
        END;
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

Different handling for different phases.

```sql
-- deploy.sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- Phase 1: Extensions
    RAISE NOTICE '=== Phase 1: Extensions ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './extensions/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 2: Migrations
    RAISE NOTICE '=== Phase 2: Migrations ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;

    -- Phase 3: Idempotent setup
    RAISE NOTICE '=== Phase 3: Setup ===';
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './setup/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
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

Or at the start of deploy.sql:

```sql
SET lock_timeout = '10s';
-- ... migrations ...
RESET lock_timeout;
```

### Concurrent index creation

For large tables, use `CONCURRENTLY` to avoid blocking:

```sql
-- 003_add_user_email_index.sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);
```

**Note:** `CREATE INDEX CONCURRENTLY` cannot run inside a transaction. Structure your deploy.sql accordingly:

```sql
-- deploy.sql: Handle concurrent index separately
-- First, run regular migrations in a transaction
BEGIN;
-- ... regular migrations ...
COMMIT;

-- Then run the concurrent index (outside transaction)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_email ON users(email);

-- Finally, continue with remaining work
BEGIN;
-- ... remaining migrations ...
COMMIT;
```

## Rollback strategies

### Automatic rollback (transaction-based)

If you use single-transaction deployment, PostgreSQL rolls back automatically on any error:

```sql
-- deploy.sql
BEGIN;
-- ... all migrations via EXECUTE v_file.content ...
COMMIT;
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
            SELECT path, content FROM pg_temp.pgmi_source_view
            WHERE path LIKE './rollback/%' AND is_sql_file
            ORDER BY path DESC
        )
        LOOP
            RAISE NOTICE 'Rolling back: %', v_file.path;
            EXECUTE v_file.content;
        END LOOP;
    ELSE
        -- Normal deployment
        FOR v_file IN (
            SELECT path, content FROM pg_temp.pgmi_plan_view
            WHERE path LIKE './migrations/%'
            ORDER BY execution_order
        )
        LOOP
            RAISE NOTICE 'Executing: %', v_file.path;
            EXECUTE v_file.content;
        END LOOP;
    END IF;
END $$;
```

Usage:
```bash
pgmi deploy . -d mydb --param rollback=true
```

### Savepoint-based progress tracking

Savepoints mark progress within a transaction. If any file fails, PostgreSQL aborts the transaction and rolls back everything — but the savepoint names in the error output tell you exactly which migration failed:

```sql
-- deploy.sql with savepoints for diagnostics
BEGIN;

DO $$
DECLARE
    v_file RECORD;
    v_savepoint TEXT;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        v_savepoint := 'migration_' || md5(v_file.path);
        EXECUTE format('SAVEPOINT %I', v_savepoint);
        RAISE NOTICE 'Running: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;

COMMIT;
```

**Note:** This is still all-or-nothing — if any migration fails, the entire transaction rolls back. For true partial progress, use [per-migration transactions](#per-migration-transactions) instead.

## Monitoring and observability

### Deployment progress

pgmi outputs PostgreSQL `RAISE NOTICE` messages. Capture them in your CI/CD:

```bash
pgmi deploy . -d mydb 2>&1 | tee deployment.log
```

### Custom progress tracking

Add notices in deploy.sql:

```sql
DO $$
DECLARE
    v_file RECORD;
    v_total INT;
    v_count INT := 0;
BEGIN
    SELECT count(*) INTO v_total FROM pg_temp.pgmi_plan_view;

    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        v_count := v_count + 1;
        RAISE NOTICE '[%/%] Executing: %', v_count, v_total, v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Audit logging

Log deployments to a table for historical tracking:

```sql
-- deploy.sql: Audit logging
DO $$
DECLARE
    v_deployment_id UUID := gen_random_uuid();
    v_file RECORD;
    v_env TEXT := pg_temp.pgmi_get_param('env', 'unknown');
    v_files_count INT;
BEGIN
    SELECT count(*) INTO v_files_count FROM pg_temp.pgmi_plan_view;

    -- Record deployment start
    INSERT INTO audit.deployments (id, started_at, env, files_count)
    VALUES (v_deployment_id, now(), v_env, v_files_count);

    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;

        -- Log file execution
        INSERT INTO audit.deployment_files (deployment_id, file_path, executed_at)
        VALUES (v_deployment_id, v_file.path, now());
    END LOOP;

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

### Azure DevOps

```yaml
steps:
  - task: AzureCLI@2
    inputs:
      azureSubscription: 'my-service-connection'
      scriptType: 'bash'
      scriptLocation: 'inlineScript'
      inlineScript: |
        pgmi deploy . -d $DATABASE_NAME \
          --host $PGHOST \
          --azure \
          --sslmode require \
          --param env=production \
          --timeout 15m
```

### GitHub Actions (Azure)

```yaml
deploy:
  runs-on: ubuntu-latest
  permissions:
    id-token: write
    contents: read
  steps:
    - uses: actions/checkout@v4

    - uses: azure/login@v2
      with:
        client-id: ${{ secrets.AZURE_CLIENT_ID }}
        tenant-id: ${{ secrets.AZURE_TENANT_ID }}
        subscription-id: ${{ secrets.AZURE_SUBSCRIPTION_ID }}

    - name: Deploy
      run: |
        pgmi deploy . -d ${{ vars.DATABASE_NAME }} \
          --host ${{ vars.AZURE_PG_HOST }} \
          --azure \
          --sslmode require \
          --param env=production \
          --timeout 15m
```

### GitHub Actions (AWS)

```yaml
deploy:
  runs-on: ubuntu-latest
  permissions:
    id-token: write
    contents: read
  steps:
    - uses: actions/checkout@v4

    - uses: aws-actions/configure-aws-credentials@v4
      with:
        role-to-assume: ${{ secrets.AWS_ROLE_ARN }}
        aws-region: ${{ vars.AWS_REGION }}

    - name: Install pgmi
      run: go install github.com/vvka-141/pgmi/cmd/pgmi@latest

    - name: Deploy
      run: |
        pgmi deploy . -d ${{ vars.DATABASE_NAME }} \
          --host ${{ vars.RDS_HOST }} \
          -U ${{ vars.RDS_USER }} \
          --aws --aws-region ${{ vars.AWS_REGION }} \
          --sslmode require \
          --param env=production \
          --timeout 15m
```

### GitHub Actions (GCP)

```yaml
deploy:
  runs-on: ubuntu-latest
  permissions:
    id-token: write
    contents: read
  steps:
    - uses: actions/checkout@v4

    - uses: google-github-actions/auth@v2
      with:
        workload_identity_provider: ${{ secrets.GCP_WORKLOAD_IDENTITY_PROVIDER }}
        service_account: ${{ secrets.GCP_SERVICE_ACCOUNT }}

    - name: Install pgmi
      run: go install github.com/vvka-141/pgmi/cmd/pgmi@latest

    - name: Deploy
      run: |
        pgmi deploy . -d ${{ vars.DATABASE_NAME }} \
          -U ${{ vars.CLOUDSQL_USER }} \
          --google --google-instance ${{ vars.CLOUDSQL_INSTANCE }} \
          --param env=production \
          --timeout 15m
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
# 1. Deploy to blue (standby) with tests gating the commit
pgmi deploy . -d mydb_blue --param env=production

# 2. If deployment succeeds (tests passed), switch traffic (application config or DNS)

# 3. Blue becomes production, green becomes standby
```

Tests run as part of deployment via `CALL pgmi_test()` in your deploy.sql. If any test fails, the deployment rolls back and traffic stays on the current production database.

## Next steps

- [Security Guide](SECURITY.md) — Secrets handling in CI/CD
- [Testing Guide](TESTING.md) — Pre-deployment testing patterns
- [Retry and Timeout Behavior](retry-timeout-behavior.md) — Detailed timeout mechanics
