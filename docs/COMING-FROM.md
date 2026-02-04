# Coming from Other Tools

This guide helps you migrate to pgmi from other database deployment tools. Each section maps familiar concepts to pgmi equivalents and shows a concrete migration path.

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
   DO $$
   DECLARE
       v_file RECORD;
   BEGIN
       -- Single transaction (like Flyway's default)
       PERFORM pg_temp.pgmi_plan_command('BEGIN;');

       -- Execute all migrations in filename order
       FOR v_file IN (
           SELECT path FROM pg_temp.pgmi_source
           WHERE directory = './migrations/' AND is_sql_file
           ORDER BY path
       )
       LOOP
           PERFORM pg_temp.pgmi_plan_file(v_file.path);
       END LOOP;

       PERFORM pg_temp.pgmi_plan_command('COMMIT;');
   END $$;
   ```

4. **Deploy**:
   ```bash
   pgmi deploy . --database mydb
   ```

### Mapping Flyway features

| Flyway feature | pgmi equivalent |
|----------------|-----------------|
| `flyway migrate` | `pgmi deploy .` |
| `flyway info` | Query `pg_temp.pgmi_source` in deploy.sql |
| `flyway validate` | `pgmi validate .` or `pgmi metadata validate .` |
| `flyway clean` | `pgmi deploy . --overwrite` (recreates DB; **local development only**) |
| `flyway_schema_history` | Implement your own tracking table, or use metadata |
| Callbacks (`beforeMigrate`, etc.) | Code in deploy.sql before/after file loops |
| Placeholders (`${var}`) | Parameters via `--param` + `pgmi_get_param()` |

### What you gain

- **Transaction flexibility**: Flyway is all-or-nothing per run. pgmi lets you commit per-migration:
  ```sql
  FOR v_file IN (SELECT path FROM pg_temp.pgmi_source WHERE is_sql_file ORDER BY path)
  LOOP
      PERFORM pg_temp.pgmi_plan_command('BEGIN;');
      PERFORM pg_temp.pgmi_plan_file(v_file.path);
      PERFORM pg_temp.pgmi_plan_command('COMMIT;');
  END LOOP;
  ```

- **Conditional logic**: Skip migrations based on environment, feature flags, or database state:
  ```sql
  IF pg_temp.pgmi_get_param('env', 'dev') = 'production' THEN
      -- Production-only migrations
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
   DO $$
   DECLARE
       v_file RECORD;
   BEGIN
       PERFORM pg_temp.pgmi_plan_command('BEGIN;');

       FOR v_file IN (
           SELECT path FROM pg_temp.pgmi_source
           WHERE directory = './migrations/' AND is_sql_file
           ORDER BY path
       )
       LOOP
           PERFORM pg_temp.pgmi_plan_file(v_file.path);
       END LOOP;

       PERFORM pg_temp.pgmi_plan_command('COMMIT;');
   END $$;
   ```

3. **Map Liquibase contexts to parameters**:

   **Before (Liquibase):**
   ```xml
   <changeSet id="1" context="production">
   ```

   **After (pgmi):**
   ```sql
   IF pg_temp.pgmi_get_param('env', 'dev') = 'production' THEN
       PERFORM pg_temp.pgmi_plan_file('./migrations/production_only.sql');
   END IF;
   ```

### Mapping Liquibase features

| Liquibase feature | pgmi equivalent |
|-------------------|-----------------|
| `liquibase update` | `pgmi deploy .` |
| `liquibase status` | `pgmi metadata plan .` or query pgmi_source |
| `liquibase rollback` | PostgreSQL transaction rollback |
| `databasechangelog` | Implement tracking table, or use pgmi metadata |
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
           SELECT path FROM pg_temp.pgmi_source
           WHERE is_sql_file
           ORDER BY path
       )
       LOOP
           RAISE NOTICE 'Executing: %', v_file.path;
           PERFORM pg_temp.pgmi_plan_file(v_file.path);
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

FOR v_file IN (SELECT path, checksum FROM pg_temp.pgmi_source WHERE ...)
LOOP
    IF NOT EXISTS (SELECT 1 FROM migration_history WHERE filename = v_file.path) THEN
        PERFORM pg_temp.pgmi_plan_file(v_file.path);
        INSERT INTO migration_history (filename, checksum) VALUES (v_file.path, v_file.checksum);
    END IF;
END LOOP;
```

## Next steps

- [Getting Started](QUICKSTART.md) — Hands-on first deployment
- [Session API Reference](session-api.md) — All temp tables and helper functions
- [Why pgmi?](WHY-PGMI.md) — When pgmi's approach makes sense
