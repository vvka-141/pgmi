# Coming from Other Tools

This guide helps you migrate to pgmi from other database deployment tools. Each section maps familiar concepts to pgmi equivalents and shows a concrete migration path.

> **How pgmi deploys:** The `deploy.sql` examples below query files from `pg_temp.pgmi_plan_view` (or `pg_temp.pgmi_source_view`) and execute them directly with `EXECUTE`. See [Session API](session-api.md) for the full reference.

## Quick concept mapping

| Concept | Flyway | Liquibase | pgmi |
|---------|--------|-----------|------|
| Migration files | `V1__name.sql` | Changelog + changesets | Any `.sql` file |
| Execution order | Filename prefix (V1, V2...) | Changelog order | Your `deploy.sql` decides |
| Transaction control | `--single-transaction` flag | Per-changeset or global | `BEGIN`/`COMMIT` in deploy.sql |
| Tracking state | `flyway_schema_history` table | `databasechangelog` table | Your choice (or none) |
| Rollback | Undo scripts (`U1__name.sql`) | Rollback commands in changeset | PostgreSQL transactions |
| Conditionals | Callbacks, limited | Preconditions, contexts | Full PL/pgSQL in deploy.sql |
| Configuration | `flyway.conf` / `flyway.toml` | `liquibase.properties` | `pgmi.yaml` |

## Coming from Flyway

### What changes

**Before (Flyway):**
```
migrations/
├── V1__create_users.sql
├── V2__add_email.sql
└── V3__create_orders.sql

flyway.conf:
flyway.url=jdbc:postgresql://localhost/mydb
flyway.user=postgres
```

**After (pgmi):**
```
myapp/
├── deploy.sql              # You write deployment logic
├── pgmi.yaml               # Connection defaults
└── migrations/
    ├── 001_create_users.sql
    ├── 002_add_email.sql
    └── 003_create_orders.sql
```

### Migration steps

1. **Rename files** (optional but cleaner):
   ```bash
   # V1__create_users.sql → 001_create_users.sql
   # The V prefix was Flyway convention; pgmi doesn't require it
   ```

2. **Create `pgmi.yaml`**:
   ```yaml
   connection:
     host: localhost
     database: mydb
     username: postgres
   ```

3. **Create `deploy.sql`** that mimics Flyway's behavior:
   ```sql
   -- deploy.sql: Flyway-like linear execution
   BEGIN;

   DO $$
   DECLARE
       v_file RECORD;
   BEGIN
       -- Execute all migrations in filename order
       FOR v_file IN (
           SELECT path, content FROM pg_temp.pgmi_plan_view
           WHERE path LIKE './migrations/%'
           ORDER BY execution_order
       )
       LOOP
           RAISE NOTICE 'Executing: %', v_file.path;
           EXECUTE v_file.content;
       END LOOP;
   END $$;

   COMMIT;
   ```

4. **Deploy**:
   ```bash
   pgmi deploy . --database mydb
   ```

### Mapping Flyway features

| Flyway feature | pgmi equivalent |
|----------------|-----------------|
| `flyway migrate` | `pgmi deploy .` |
| `flyway info` | Query `pg_temp.pgmi_source_view` in deploy.sql |
| `flyway validate` | `pgmi metadata validate .` |
| `flyway clean` | `pgmi deploy . --overwrite` (recreates DB; **local development only**) |
| `flyway_schema_history` | Implement your own tracking table, or use [pgmi metadata](METADATA.md) |
| Callbacks (`beforeMigrate`, etc.) | Code in deploy.sql before/after file loops |
| Placeholders (`${var}`) | Parameters via `--param` + `current_setting('pgmi.key', true)` |

### What you gain

- **Transaction flexibility**: Flyway is all-or-nothing per run. pgmi lets you commit per-migration:
  ```sql
  FOR v_file IN (
      SELECT path, content FROM pg_temp.pgmi_plan_view
      WHERE path LIKE './migrations/%' ORDER BY execution_order
  )
  LOOP
      BEGIN
          EXECUTE v_file.content;
      EXCEPTION WHEN OTHERS THEN
          RAISE EXCEPTION 'Failed on %: %', v_file.path, SQLERRM;
      END;
  END LOOP;
  ```

- **Conditional logic**: Skip migrations based on environment, feature flags, or database state:
  ```sql
  IF COALESCE(current_setting('pgmi.env', true), 'dev') = 'production' THEN
      FOR v_file IN (SELECT path, content FROM pg_temp.pgmi_plan_view WHERE path LIKE './production/%') LOOP
          EXECUTE v_file.content;
      END LOOP;
  END IF;
  ```

- **No Java dependency**: pgmi is a single Go binary.

## Coming from Liquibase

### What changes

**Before (Liquibase):**
```
db/
├── changelog.xml
├── changes/
│   ├── 001-create-users.xml
│   └── 002-add-email.xml
└── liquibase.properties
```

**After (pgmi):**
```
myapp/
├── deploy.sql
├── pgmi.yaml
└── migrations/
    ├── 001_create_users.sql
    └── 002_add_email.sql
```

### Migration steps

1. **Convert changesets to SQL files**:

   **Before (Liquibase XML):**
   ```xml
   <changeSet id="1" author="dev">
       <createTable tableName="users">
           <column name="id" type="serial" autoIncrement="true">
               <constraints primaryKey="true"/>
           </column>
           <column name="email" type="varchar(255)"/>
       </createTable>
   </changeSet>
   ```

   **After (plain SQL):**
   ```sql
   -- 001_create_users.sql
   CREATE TABLE users (
       id SERIAL PRIMARY KEY,
       email VARCHAR(255)
   );
   ```

2. **Create `deploy.sql`**:
   ```sql
   BEGIN;

   DO $$
   DECLARE
       v_file RECORD;
   BEGIN
       FOR v_file IN (
           SELECT path, content FROM pg_temp.pgmi_plan_view
           WHERE path LIKE './migrations/%'
           ORDER BY execution_order
       )
       LOOP
           RAISE NOTICE 'Executing: %', v_file.path;
           EXECUTE v_file.content;
       END LOOP;
   END $$;

   COMMIT;
   ```

3. **Map Liquibase contexts to parameters**:

   **Before (Liquibase):**
   ```xml
   <changeSet id="1" context="production">
   ```

   **After (pgmi):**
   ```sql
   IF COALESCE(current_setting('pgmi.env', true), 'dev') = 'production' THEN
       FOR v_file IN (SELECT content FROM pg_temp.pgmi_source_view WHERE path = './migrations/production_only.sql') LOOP
           EXECUTE v_file.content;
       END LOOP;
   END IF;
   ```

### Mapping Liquibase features

| Liquibase feature | pgmi equivalent |
|-------------------|-----------------|
| `liquibase update` | `pgmi deploy .` |
| `liquibase status` | `pgmi metadata plan .` or query `pg_temp.pgmi_source_view` |
| `liquibase rollback` | PostgreSQL transaction rollback |
| `databasechangelog` | Implement tracking table, or use [pgmi metadata](METADATA.md) |
| Contexts | Parameters + conditionals in deploy.sql |
| Preconditions | PL/pgSQL conditionals in deploy.sql |
| Labels | Query file paths/names in deploy.sql |

### What you gain

- **No XML/YAML/JSON**: Pure SQL files, no framework markup
- **Full PostgreSQL power**: Use any PostgreSQL feature, not just what Liquibase supports
- **Simpler debugging**: Errors are PostgreSQL errors, not Liquibase interpretation errors

## Coming from raw psql scripts

If you're currently running SQL files manually with `psql`, pgmi adds structure without complexity.

### What changes

**Before:**
```bash
psql -d mydb -f 001_create_users.sql
psql -d mydb -f 002_add_email.sql
psql -d mydb -f 003_create_orders.sql
```

**After:**
```bash
pgmi deploy . --database mydb
```

### Migration steps

1. **Organize files**:
   ```
   myapp/
   ├── deploy.sql
   ├── pgmi.yaml
   └── migrations/
       ├── 001_create_users.sql
       ├── 002_add_email.sql
       └── 003_create_orders.sql
   ```

2. **Create minimal `deploy.sql`**:
   ```sql
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
   ```

### What you gain

- **Atomic deployments**: Wrap everything in a transaction
- **Parameterization**: Pass environment-specific values via `--param`
- **Testing**: Add `__test__/` or `__tests__/` directories with automatic rollback
- **Reproducibility**: Same deploy.sql, same behavior

## Tracking migration state

Unlike Flyway and Liquibase, pgmi doesn't mandate a tracking table. You have options:

### Option 1: No tracking (idempotent scripts)

Write scripts that can run multiple times safely:
```sql
-- 001_create_users.sql
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255)
);
```

### Option 2: Use pgmi metadata

Add UUID-based tracking with the advanced template:
```sql
/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="false">
</pgmi-meta>
*/
ALTER TABLE users ADD COLUMN phone TEXT;
```

See [Metadata Guide](METADATA.md) for details.

### Option 3: Custom tracking table

Implement your own, like Flyway does:
```sql
-- In deploy.sql
CREATE TABLE IF NOT EXISTS migration_history (
    id SERIAL PRIMARY KEY,
    filename TEXT NOT NULL UNIQUE,
    checksum TEXT NOT NULL,
    applied_at TIMESTAMPTZ DEFAULT now()
);

BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content, checksum FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        IF NOT EXISTS (SELECT 1 FROM migration_history WHERE filename = v_file.path) THEN
            RAISE NOTICE 'Executing: %', v_file.path;
            EXECUTE v_file.content;
            INSERT INTO migration_history (filename, checksum) VALUES (v_file.path, v_file.checksum);
        ELSE
            RAISE NOTICE 'Skipping (already applied): %', v_file.path;
        END IF;
    END LOOP;
END $$;

COMMIT;
```

## Next steps

- [Getting Started](QUICKSTART.md) — Hands-on first deployment
- [Session API Reference](session-api.md) — All temp tables and helper functions
- [Why pgmi?](WHY-PGMI.md) — When pgmi's approach makes sense
