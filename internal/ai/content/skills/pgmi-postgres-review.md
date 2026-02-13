---
name: pgmi-postgres-review
description: "Use when reviewing SQL/PL/pgSQL for correctness and performance"
user_invocable: true
---


**Purpose**: Domain expertise for reviewing PostgreSQL SQL/PL/pgSQL code. Ensures scripts are enterprise-grade, performant, secure, and appreciated by expert DBAs.

**Used By**:
- postgres-sql-reviewer (primary - code review)
- http-expert-reviewer (secondary - transactional HTTP patterns)
- general-purpose (when writing SQL/PL/pgSQL)
- change-planner (when planning SQL changes)

**Depends On**: pgmi-review-philosophy, pgmi-security-review, pgmi-sql (if exists)

**Auto-Load Skills**:
- `pgmi-sql-change-protocol` (mandatory testing workflow for SQL changes)

**Auto-Load With**:
- `pgmi-sql` skill (SQL coding work)
- File patterns: `**/*.sql` (when writing SQL)
- Keywords: "migration", "query", "performance", "RLS", "grants"

**Load For**: SQL vs PL/pgSQL decisions, query optimization, enterprise DBA requirements

---

## Verification Requirements (MANDATORY)

### Custom PostgreSQL Objects Awareness

**PostgreSQL is highly extensible**. Projects often define custom operators, types, and functions.
Before flagging "unknown" or "non-standard" objects, **ALWAYS verify they're not custom definitions**.

#### Custom Operators

PostgreSQL allows custom operators for domain-specific syntax:

```sql
-- Example: pgmi advanced template defines try-cast operator
CREATE OPERATOR ?| (
    LEFTARG = text,
    RIGHTARG = uuid,
    PROCEDURE = utils.try_cast
);

-- Later usage is VALID if operator defined first
SELECT metadata->>'id' ?| NULL::uuid;  -- ✓ Valid with custom ?| operator
```

**Verification Steps**:
1. Search codebase for `CREATE OPERATOR <symbol>` (e.g., `CREATE OPERATOR ?|`)
2. Check metadata `<sortKeys>` to verify execution order (lower = earlier)
3. Verify operator is defined before used
4. **Only flag if**: Operator used but never defined, OR defined after usage

#### Try-Cast with NULL Sentinel Pattern

**IMPORTANT**: Using `NULL` as the default value in try-cast is a **valid and intentional pattern** for explicit error handling:

```sql
-- ✅ CORRECT PATTERN: NULL sentinel for explicit error handling
v_id := metadata->>'id' ?| NULL::uuid;

IF v_id IS NULL THEN
    IF metadata->'id' IS NULL THEN
        RAISE EXCEPTION 'Route id is required in metadata';
    ELSE
        RAISE EXCEPTION 'Route id must be a valid UUID, got: "%"', metadata->>'id';
    END IF;
END IF;
```

**Why This Works**:
1. If input is valid UUID → returns the UUID (not NULL)
2. If input is missing/invalid → returns NULL (the default)
3. Code then checks for NULL and provides distinct error messages for "missing" vs "invalid format"

**DO NOT FLAG as bugs**:
- `value ?| NULL::uuid` followed by explicit NULL check
- `COALESCE(value ?| NULL::boolean, false)` - NULL sentinel with outer fallback
- Any `?| NULL::type` pattern with subsequent validation logic

**Only flag if**: NULL result is used without validation (silent failure).

**Common Custom Operators in Sophisticated Codebases**:
- `?|` - Try-cast with default
- `=>` - Key-value pairs (hstore)
- `@>` / `<@` - Containment (jsonb, arrays)
- `||` - Concatenation (arrays, strings)
- Custom domain-specific operators

#### Custom Types

Composite types, enums, and domains are common in enterprise PostgreSQL:

```sql
-- Example: HTTP request type
CREATE TYPE api.http_request AS (
    method TEXT,
    path TEXT,
    headers JSONB,
    body JSONB
);

-- Usage is valid if type defined first
CREATE FUNCTION api.process_request(req api.http_request)
RETURNS api.http_response AS $$ ... $$;
```

**Verification Steps**:
1. Search for `CREATE TYPE <name>` / `CREATE DOMAIN <name>`
2. Check execution order via `<sortKeys>`
3. **Only flag if**: Type used but never defined

#### Custom Functions

User-defined functions are the norm, not the exception:

```sql
-- Example: Safe type casting with defaults
CREATE FUNCTION utils.try_cast(input text, default_value uuid)
RETURNS uuid AS $$
    SELECT CASE
        WHEN $1 ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
        THEN $1::uuid
        ELSE $2
    END;
$$ LANGUAGE SQL IMMUTABLE;

-- Usage is valid if function defined first
SELECT utils.try_cast('invalid-uuid', extensions.uuid_nil());
```

**Verification Steps**:
1. Search for `CREATE FUNCTION <schema>.<name>`
2. Don't assume all functions are in `pg_catalog`
3. **Only flag if**: Function called but never defined

### Understanding pgmi Execution Order

pgmi uses **metadata sortKeys** in `<pgmi-meta>` blocks to control file execution order:

```xml
<!-- cast_utils.sql -->
<pgmi-meta>
  <sortKeys>
    <key>001/000</key>  <!-- Executes FIRST -->
  </sortKeys>
</pgmi-meta>

<!-- foundation.sql -->
<pgmi-meta>
  <sortKeys>
    <key>004/000</key>  <!-- Executes FOURTH (after 001, 002, 003) -->
  </sortKeys>
</pgmi-meta>
```

**Rule**: **Lower sortKey number = Earlier execution**

**Before flagging dependency issues**:
1. Find both files' `<pgmi-meta>` blocks
2. Extract `<sortKeys>` values
3. Compare: dependency must have lower sortKey than usage
4. **Only flag if**: Usage sortKey < Definition sortKey (wrong order)

**Example Verification**:
```
cast_utils.sql:    sortKey 001/000 (defines ?| operator)
foundation.sql:    sortKey 004/000 (uses ?| operator)
001 < 004 → ✓ Correct order, no issue
```

---

## Critical Patterns (High-Risk Areas)

### EXECUTE...INTO with Composite Type Returns

**SEVERITY: 🔴 CRITICAL** - This pattern causes silent type conversion failures that break handler execution

**The Problem**: PostgreSQL's EXECUTE with `SELECT function()` attempts to destructure composite type returns incorrectly, causing type parsing errors.

**❌ REJECT** (causes runtime failure):
```sql
DECLARE
    v_response api.http_response;  -- Composite type
    v_sql text;
BEGIN
    v_sql := format('SELECT api.%I($1::api.http_request)', handler_name);
    EXECUTE v_sql INTO v_response USING v_request;
    -- ERROR: invalid input syntax for type integer: "(200, headers, content)"
END;
```

**✅ REQUIRE** (correct pattern):
```sql
DECLARE
    v_response api.http_response;  -- Composite type
    v_sql text;
BEGIN
    v_sql := format('SELECT * FROM api.%I($1::api.http_request)', handler_name);
    EXECUTE v_sql INTO v_response USING v_request;
    -- Success: properly destructures composite type
END;
```

**Why This Matters**:
- Composite types convert to text as tuples: `(field1, field2, field3)`
- EXECUTE with `SELECT function()` tries to assign this text to the first field
- Results in type conversion error: trying to parse tuple syntax as target type
- Using `SELECT * FROM function()` treats function as table-returning, preserving composite structure

**Detection**:
- Look for `EXECUTE` statements with `INTO` clause
- Check if target variable is a composite type (CREATE TYPE)
- Verify pattern: must be `SELECT * FROM` not `SELECT`

**Reference**: `.claude/skills/reference/postgresql-patterns.md` → "Dynamic SQL with EXECUTE"

**Real Incident**: HTTP framework failure (2025-11-22) - Handler executed correctly but response wasn't captured due to this pattern.

### format() Placeholder Misuse

**SEVERITY: 🟠 HIGH** - Incorrect quoting breaks SQL and creates confusion

**The Problem**: Using `%I` (identifier placeholder) for type names causes incorrect quoting.

**❌ REJECT**:
```sql
format('SELECT ($1::%I)', 'api.http_request')
-- Produces: SELECT ($1::"api.http_request")
-- Error: type "api.http_request" does not exist (quotes make it identifier)
```

**✅ REQUIRE**:
```sql
format('SELECT ($1::api.http_request)')
-- Produces: SELECT ($1::api.http_request)
-- Success: type name used directly
```

**Rule**: `%I` is for identifiers (tables, columns, schemas). Type names should be hardcoded or validated separately.

**Reference**: `.claude/skills/reference/postgresql-patterns.md` → "Type Handling in format()"

### Row-by-Row Processing

**SEVERITY: 🟡 MEDIUM** - Performance issue in production

**❌ REJECT** (slow for large datasets):
```sql
FOR v_record IN SELECT * FROM users LOOP
    UPDATE users SET last_seen = now() WHERE id = v_record.id;
END LOOP;
-- N separate UPDATE statements
```

**✅ REQUIRE** (set-based):
```sql
UPDATE users SET last_seen = now();
-- Single UPDATE statement
```

**When Loops Are Necessary**: Use array_agg and bulk operations.

**Reference**: `.claude/skills/reference/postgresql-patterns.md` → "Performance Patterns"

---

### Severity Calibration for PostgreSQL

**🔴 Critical** = Actual bug/vulnerability **NOW**:
- ✅ SQL injection with **parameterized** user input
- ✅ Missing RLS on **production** multi-tenant table with **real** data
- ✅ Data corruption in **active** write path
- ✅ Syntax error **preventing** deployment
- ❌ NOT: "If this variable were user input..." ← Suggestion
- ❌ NOT: "Non-standard operator" when operator defined elsewhere ← False positive

**🟠 Major** = Significant **real** issue:
- ✅ N+1 query in loop over **1000+** items
- ✅ Missing index on **large table** (>1M rows) foreign key
- ✅ PL/pgSQL loop where SQL set operation objectively better
- ✅ Exception swallowing hiding **critical** errors
- ❌ NOT: N+1 over 10 items ← Minor or Suggestion
- ❌ NOT: "Could be slow at scale" ← Suggestion

**💡 Suggestion** = Hypothetical or optional:
- ✅ "**IF** this becomes parameterized, validate input"
- ✅ "**CONSIDER** caching if called frequently"
- ✅ "**COULD** extract to function for reuse"
- ✅ Must use "IF", "COULD", "CONSIDER" language

### Evidence Requirements for PostgreSQL Issues

**EVERY issue** flagged must include:

1. **Quote exact code** with file and line number
2. **Explain WHY** it's a problem (not just "this pattern is bad")
3. **Show IMPACT** with real consequences
4. **Provide FIX** with working code example
5. **Reference PRINCIPLE** from this skill or pgmi-review-philosophy

**Example of PROPER issue report**:
```markdown
**[deploy.sql:45]** PL/pgSQL Loop Over Set Operation

- **Problem**: Lines 45-52 use FOR loop to insert files:
  \```sql
  FOR v_file IN SELECT * FROM pg_temp.pgmi_source LOOP
      INSERT INTO pg_temp.pgmi_plan VALUES (v_file.content);
  END LOOP;
  \```

- **Evidence**: Processes 1000 files with 1000 INSERT statements.
  Measured: ~3 seconds vs <100ms for set operation.

- **Impact**: 30x slower for typical deployment (100-1000 files).
  Poor user experience, extends deployment window.

- **Fix**: Use single INSERT...SELECT:
  \```sql
  INSERT INTO pg_temp.pgmi_plan (command_sql)
  SELECT content FROM pg_temp.pgmi_source ORDER BY path;
  \```

- **Rationale**: "Set Operations Over Loops" (pgmi-postgres-review:184-201).
  SQL set operations are declarative, optimizable, single round-trip.

- **Severity**: 🟠 Major (real performance impact at actual scale)
```

---

## SQL vs PL/pgSQL Decision Framework

### Prefer SQL When...

**✅ Set-Based Operations**
```sql
-- Pure SQL: Declarative, optimizable, inline-able
SELECT path, content
FROM pg_temp.pgmi_source
WHERE directory = './migrations/' AND is_sql_file
ORDER BY path;
```

**✅ Simple Transformations**
```sql
-- SQL for data transformations
SELECT
    id,
    UPPER(name) AS normalized_name,
    created_at::DATE AS created_date
FROM users;
```

**✅ Aggregations and Window Functions**
```sql
-- SQL excels at aggregation
SELECT
    category,
    COUNT(*) AS total,
    ROW_NUMBER() OVER (PARTITION BY category ORDER BY created_at) AS seq
FROM items
GROUP BY category;
```

**✅ Inline Functions (Performance-Critical)**
```sql
-- SQL IMMUTABLE functions can be inlined by query planner
CREATE FUNCTION get_migration_count()
RETURNS BIGINT AS $$
    SELECT COUNT(*) FROM public.migration_script;
$$ LANGUAGE SQL STABLE;
```

### Use PL/pgSQL When...

**✅ Complex Control Flow**
```sql
-- PL/pgSQL for conditionals, loops, exception handling
CREATE FUNCTION execute_migration_script(p_path TEXT)
RETURNS INTEGER AS $$
DECLARE
    v_content TEXT;
    v_checksum TEXT;
BEGIN
    -- Fetch script
    SELECT content, checksum INTO v_content, v_checksum
    FROM public.migration_script
    WHERE path = p_path;

    -- Check if already executed
    IF EXISTS (
        SELECT 1 FROM internal.deployment_script_execution_log
        WHERE script_id = p_path AND checksum = v_checksum
    ) THEN
        RAISE NOTICE 'Skipping % (already executed)', p_path;
        RETURN -1; -- Skipped
    END IF;

    -- Execute
    EXECUTE v_content;

    -- Track execution
    INSERT INTO internal.deployment_script_execution_log (script_id, checksum)
    VALUES (p_path, v_checksum);

    RETURN 0; -- Success
EXCEPTION
    WHEN OTHERS THEN
        RAISE EXCEPTION 'Migration failed: % (SQLSTATE: %)', SQLERRM, SQLSTATE;
END;
$$ LANGUAGE plpgsql;
```

**✅ Dynamic SQL**
```sql
-- PL/pgSQL for EXECUTE with proper quoting
CREATE FUNCTION create_schema_if_missing(p_schema_name TEXT)
RETURNS VOID AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_namespace WHERE nspname = p_schema_name
    ) THEN
        EXECUTE format('CREATE SCHEMA %I', p_schema_name);
    END IF;
END;
$$ LANGUAGE plpgsql;
```

**✅ Transaction Control in Procedures**
```sql
-- Only PL/pgSQL procedures can use COMMIT/ROLLBACK
CREATE PROCEDURE deploy_with_phased_commits()
LANGUAGE plpgsql AS $$
DECLARE
    v_file RECORD;
BEGIN
    -- Phase 1: Pre-deployment
    FOR v_file IN (SELECT content FROM pg_temp.pgmi_plan_view WHERE path = './pre-deployment/setup.sql') LOOP
        EXECUTE v_file.content;
    END LOOP;
    COMMIT;

    -- Phase 2: Migrations
    FOR v_file IN (SELECT content FROM pg_temp.pgmi_plan_view WHERE path LIKE './migrations/%' ORDER BY execution_order) LOOP
        EXECUTE v_file.content;
    END LOOP;
    COMMIT;
END;
$$;
```

### Decision Matrix

| Requirement | SQL | PL/pgSQL |
|-------------|-----|----------|
| Set operations | ✅ | ❌ |
| Simple CASE expressions | ✅ | ❌ |
| Performance-critical (inlining) | ✅ | ❌ |
| IF/THEN/ELSE | ❌ | ✅ |
| Loops (FOR, WHILE) | ❌ | ✅ |
| Exception handling | ❌ | ✅ |
| EXECUTE (dynamic SQL) | ❌ | ✅ |
| Transaction control | ❌ | ✅ (procedures only) |
| Variable assignments | ❌ | ✅ |

---

## Performance Patterns

### ✅ Use CTEs for Readability
```sql
-- Good: Readable, maintainable
WITH pending_migrations AS (
    SELECT s.path, s.content, s.checksum
    FROM pg_temp.pgmi_source s
    LEFT JOIN internal.deployment_script_execution_log e
        ON s.path = e.script_id AND s.checksum = e.checksum
    WHERE e.script_id IS NULL
)
SELECT * FROM pending_migrations
ORDER BY path;
```

### ⚠️ CTE Optimization Fences
```sql
-- Be aware: CTEs act as optimization fences in PostgreSQL <12
-- Consider inline subquery for performance-critical queries

-- If you need optimization barrier (intentional):
WITH RECURSIVE ...

-- If you want optimization (PostgreSQL 12+):
SELECT ... FROM (
    -- Inline subquery (optimizer can push predicates)
) AS subquery
WHERE ...;
```

### ✅ Set Operations Over Loops
```sql
-- ❌ BAD: Procedural loop
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN SELECT * FROM pg_temp.pgmi_source LOOP
        INSERT INTO pg_temp.pgmi_plan (sequence_number, command_sql)
        VALUES (DEFAULT, v_file.content);
    END LOOP;
END $$;

-- ✅ GOOD: Single set operation
INSERT INTO pg_temp.pgmi_plan (sequence_number, command_sql)
SELECT ROW_NUMBER() OVER (ORDER BY path), content
FROM pg_temp.pgmi_source;
```

### ✅ LATERAL Joins for Correlated Subqueries
```sql
-- Efficient correlated subquery with LATERAL
SELECT s.path, latest.execution_order
FROM pg_temp.pgmi_source s
LEFT JOIN LATERAL (
    SELECT execution_order
    FROM internal.deployment_script_execution_log e
    WHERE e.script_id = s.path
    ORDER BY execution_order DESC
    LIMIT 1
) latest ON true;
```

### ✅ Index-Friendly Queries
```sql
-- ❌ BAD: Function on indexed column prevents index usage
SELECT * FROM migration_script
WHERE LOWER(path) = 'migrations/001.sql';

-- ✅ GOOD: Function on literal allows index usage
SELECT * FROM migration_script
WHERE path = LOWER('migrations/001.sql');

-- ✅ BETTER: Case-insensitive index
CREATE INDEX idx_migration_path_ci ON migration_script (LOWER(path));
SELECT * FROM migration_script
WHERE LOWER(path) = LOWER('migrations/001.sql');
```

### Performance Review Checklist
- [ ] Set operations used instead of loops where possible?
- [ ] CTEs used for readability, not assumed to optimize?
- [ ] Index-friendly predicates (no functions on indexed columns)?
- [ ] LATERAL used for efficient correlated subqueries?
- [ ] Appropriate IMMUTABLE/STABLE/VOLATILE markers?
- [ ] Large result sets paginated or streamed?

---

## Security Deep Dive

### Row-Level Security (RLS)

**Pattern**: Transparent row filtering based on current user.

```sql
-- Enable RLS on table
ALTER TABLE sensitive_data ENABLE ROW LEVEL SECURITY;

-- Policy: Users can only see their own rows
CREATE POLICY user_isolation ON sensitive_data
    FOR ALL
    TO app_user_role
    USING (user_id = current_setting('app.current_user_id')::UUID);

-- Policy: Admins see everything
CREATE POLICY admin_all_access ON sensitive_data
    FOR ALL
    TO admin_role
    USING (true);
```

**Review Questions**:
- [ ] Is RLS enabled on tables with user-scoped data?
- [ ] Are policies tested with different roles?
- [ ] Are there bypass policies for admin/service accounts?
- [ ] Are policies efficient (avoid N+1, use indexes)?

### Column-Level Security (Grants)

**Pattern**: Fine-grained access control on columns.

```sql
-- Grant table access but restrict sensitive columns
GRANT SELECT (id, name, email) ON users TO app_user_role;
GRANT SELECT (id, name, email, ssn, credit_card) ON users TO admin_role;

-- Update permissions by column
GRANT UPDATE (name, email) ON users TO app_user_role;
GRANT UPDATE (name, email, ssn, credit_card) ON users TO admin_role;
```

**Review Questions**:
- [ ] Are sensitive columns (PII, credentials) restricted?
- [ ] Do service accounts have minimal necessary grants?
- [ ] Are column-level grants tested in integration tests?

### Encryption

**At Rest** (Transparent Data Encryption):
- Review: Are pgcrypto functions used for sensitive columns?
- Pattern: `pgcrypto.encrypt(data, key, 'aes')`

**In Transit** (SSL/TLS):
- Review: Does connection string enforce SSL? (`sslmode=require`)
- Pattern: Certificate validation, not just encryption

**Application-Level Encryption**:
```sql
-- Encrypt before storage
INSERT INTO secure_vault (user_id, encrypted_secret)
VALUES (
    $1,
    pgp_sym_encrypt($2, current_setting('app.encryption_key'))
);

-- Decrypt on retrieval
SELECT pgp_sym_decrypt(encrypted_secret, current_setting('app.encryption_key'))
FROM secure_vault
WHERE user_id = $1;
```

### Grant Hierarchies and Role Design

**Best Practice**: Role hierarchy with inheritance.

```sql
-- Base roles (no LOGIN)
CREATE ROLE app_read_role;
CREATE ROLE app_write_role;
CREATE ROLE app_admin_role;

-- Grant hierarchy
GRANT app_read_role TO app_write_role;
GRANT app_write_role TO app_admin_role;

-- Actual users (LOGIN)
CREATE ROLE app_user LOGIN PASSWORD '...' IN ROLE app_write_role;
CREATE ROLE app_admin LOGIN PASSWORD '...' IN ROLE app_admin_role;
```

**Review Questions**:
- [ ] Are roles organized hierarchically (read → write → admin)?
- [ ] Do functional roles (no LOGIN) separate from user accounts (LOGIN)?
- [ ] Are default privileges set for new objects?
- [ ] Is principle of least privilege followed?

### SQL Injection Prevention

**Pattern**: Always use parameterized queries or proper quoting.

```sql
-- ❌ CRITICAL: SQL injection vulnerability
CREATE FUNCTION unsafe_query(p_table_name TEXT)
RETURNS SETOF RECORD AS $$
BEGIN
    RETURN QUERY EXECUTE 'SELECT * FROM ' || p_table_name;
END;
$$ LANGUAGE plpgsql;

-- ✅ SAFE: format() with %I for identifiers
CREATE FUNCTION safe_query(p_table_name TEXT)
RETURNS SETOF RECORD AS $$
BEGIN
    RETURN QUERY EXECUTE format('SELECT * FROM %I', p_table_name);
END;
$$ LANGUAGE plpgsql;

-- ✅ SAFE: format() with %L for literals
CREATE FUNCTION safe_where(p_value TEXT)
RETURNS SETOF users AS $$
BEGIN
    RETURN QUERY EXECUTE format('SELECT * FROM users WHERE name = %L', p_value);
END;
$$ LANGUAGE plpgsql;
```

**Review Checklist**:
- [ ] No string concatenation for SQL construction?
- [ ] `format(%I)` used for identifiers (tables, columns, schemas)?
- [ ] `format(%L)` used for literals (strings, numbers)?
- [ ] User input never directly interpolated into SQL?

---

## Enterprise DBA Expectations

### Audit Trails

**Pattern**: Track all DDL and critical DML.

```sql
-- Execution log (who, what, when)
CREATE TABLE internal.deployment_script_execution_log (
    execution_order SERIAL PRIMARY KEY,
    script_id TEXT NOT NULL,
    checksum TEXT NOT NULL,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_by TEXT NOT NULL DEFAULT current_user,
    execution_duration INTERVAL,
    status TEXT CHECK (status IN ('success', 'failed', 'skipped'))
);

-- Change tracking (before/after)
CREATE TABLE audit.schema_changes (
    change_id BIGSERIAL PRIMARY KEY,
    change_type TEXT NOT NULL, -- 'CREATE', 'ALTER', 'DROP'
    object_type TEXT NOT NULL, -- 'TABLE', 'INDEX', 'FUNCTION'
    object_name TEXT NOT NULL,
    ddl_command TEXT,
    changed_at TIMESTAMPTZ DEFAULT now(),
    changed_by TEXT DEFAULT current_user
);
```

**Review Questions**:
- [ ] Are deployments logged with timestamps and user?
- [ ] Are failures captured with error details?
- [ ] Can audit trail reconstruct deployment history?
- [ ] Are logs retained per compliance requirements?

### Migration Safety

**Pattern**: Idempotent, reversible, testable.

```sql
-- Idempotent DDL
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL
);

-- Reversible with explicit rollback guidance
-- Migration: 002_add_email_column.sql
ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT;

-- Rollback: Documented in migration or separate file
-- ALTER TABLE users DROP COLUMN IF EXISTS email;
```

**Review Checklist**:
- [ ] All DDL uses IF EXISTS / IF NOT EXISTS?
- [ ] Destructive operations (DROP, TRUNCATE) clearly marked?
- [ ] Data migrations preserve existing data?
- [ ] Rollback strategy documented?
- [ ] Migrations tested in transaction (can rollback)?

### Compliance Requirements

**Common Standards**: SOC 2, HIPAA, GDPR, PCI-DSS.

**Review Areas**:
- [ ] PII columns identified and protected (encryption, grants)?
- [ ] Data retention policies enforced (TTL, soft deletes)?
- [ ] Access logs captured (who accessed what, when)?
- [ ] Consent tracking (GDPR right to erasure)?
- [ ] Secure defaults (passwords not in plaintext, etc.)?

### Rollback Strategies

**Pattern 1: Transactional Rollback**
```sql
BEGIN;
    -- Migration DDL
    ALTER TABLE users ADD COLUMN new_field TEXT;

    -- Validation
    DO $$
    BEGIN
        IF NOT EXISTS (
            SELECT 1 FROM information_schema.columns
            WHERE table_name = 'users' AND column_name = 'new_field'
        ) THEN
            RAISE EXCEPTION 'Migration validation failed';
        END IF;
    END $$;
COMMIT; -- Or ROLLBACK if validation fails
```

**Pattern 2: Compensating Transactions**
```sql
-- Forward migration: 003_archive_old_data.sql
INSERT INTO archive.old_users SELECT * FROM public.users WHERE created_at < '2020-01-01';
DELETE FROM public.users WHERE created_at < '2020-01-01';

-- Rollback migration: 003_restore_old_data.sql (if needed)
INSERT INTO public.users SELECT * FROM archive.old_users;
DELETE FROM archive.old_users;
```

---

## PostgreSQL-Specific Anti-Patterns

### ❌ Cursor Abuse
```sql
-- ❌ BAD: Cursor for simple iteration
DECLARE
    cur CURSOR FOR SELECT * FROM users;
    rec RECORD;
BEGIN
    OPEN cur;
    LOOP
        FETCH cur INTO rec;
        EXIT WHEN NOT FOUND;
        -- Process rec
    END LOOP;
    CLOSE cur;
END;

-- ✅ GOOD: FOR loop (implicit cursor, cleaner)
DECLARE
    rec RECORD;
BEGIN
    FOR rec IN SELECT * FROM users LOOP
        -- Process rec
    END LOOP;
END;

-- ✅ BETTER: Set operation (no loop at all)
INSERT INTO processed_users
SELECT id, UPPER(name)
FROM users;
```

### ❌ Unnecessary Dynamic SQL
```sql
-- ❌ BAD: Dynamic SQL for static query
EXECUTE 'SELECT * FROM users WHERE id = ' || user_id;

-- ✅ GOOD: Static SQL (better performance, safer)
SELECT * FROM users WHERE id = user_id;
```

### ❌ Transaction Anti-Patterns
```sql
-- ❌ BAD: Transaction per row (high overhead)
FOR rec IN SELECT * FROM users LOOP
    BEGIN
        UPDATE users SET processed = true WHERE id = rec.id;
        COMMIT; -- Can't COMMIT in function!
    END;
END LOOP;

-- ✅ GOOD: Single transaction for batch
UPDATE users SET processed = true
WHERE id IN (SELECT id FROM users WHERE NOT processed);
COMMIT;
```

### ❌ Silent Failures
```sql
-- ❌ BAD: Swallow all errors
BEGIN
    -- Risky operation
EXCEPTION
    WHEN OTHERS THEN
        -- Silently ignore
END;

-- ✅ GOOD: Specific exception handling with logging
BEGIN
    -- Risky operation
EXCEPTION
    WHEN unique_violation THEN
        RAISE WARNING 'Duplicate key detected: %', SQLERRM;
    WHEN OTHERS THEN
        RAISE EXCEPTION 'Unexpected error: % (SQLSTATE: %)', SQLERRM, SQLSTATE;
END;
```

### ❌ Overuse of VOLATILE
```sql
-- ❌ BAD: Default VOLATILE for deterministic function
CREATE FUNCTION calculate_total(p_quantity INT, p_price NUMERIC)
RETURNS NUMERIC AS $$
    SELECT p_quantity * p_price;
$$ LANGUAGE SQL; -- Defaults to VOLATILE (inefficient)

-- ✅ GOOD: IMMUTABLE for deterministic function (allows inlining)
CREATE FUNCTION calculate_total(p_quantity INT, p_price NUMERIC)
RETURNS NUMERIC AS $$
    SELECT p_quantity * p_price;
$$ LANGUAGE SQL IMMUTABLE;
```

**Volatility Guide**:
- **IMMUTABLE**: Pure function (same inputs → same outputs, no DB access)
- **STABLE**: Reads DB but doesn't modify (consistent within transaction)
- **VOLATILE**: Modifies DB or has side effects (default for PL/pgSQL)

---

## pgmi-Specific PostgreSQL Patterns

### Direct Execution Pattern
```sql
-- pgmi uses direct execution with EXECUTE
-- Query files from pgmi_plan_view and execute directly
FOR v_file IN (
    SELECT path, content FROM pg_temp.pgmi_plan_view
    ORDER BY execution_order
)
LOOP
    RAISE NOTICE 'Executing: %', v_file.path;
    EXECUTE v_file.content;
END LOOP;
```

### Dollar-Quoting Consistency
```sql
-- Always use $$ for multi-line dynamic SQL
EXECUTE format($sql$
    CREATE TABLE %I (
        id SERIAL PRIMARY KEY,
        name TEXT NOT NULL
    )
$sql$, v_table_name);
```

### Temporary Table Discipline
```sql
-- pgmi uses pg_temp.* for session-scoped state
-- Review: Are temp tables properly scoped?
-- Review: Are temp tables cleaned up (they auto-cleanup on session end)?

CREATE TEMP TABLE IF NOT EXISTS pg_temp.my_working_data (
    id INT,
    value TEXT
);
```

---

## JSON/JSONB Naming Conventions

**MANDATORY: All JSON/JSONB keys use camelCase, not snake_case.**

PostgreSQL identifiers (tables, columns, functions) use snake_case. JSON content follows JSON/JavaScript conventions (camelCase). These are separate domains.

### The Rule

```sql
-- ❌ REJECT: snake_case in JSON keys
jsonb_build_object(
    'http_method', '^GET$',      -- Wrong
    'auto_log', false,            -- Wrong
    'input_schema', '{}'::jsonb   -- Wrong
)

-- ✅ REQUIRE: camelCase in JSON keys
jsonb_build_object(
    'httpMethod', '^GET$',       -- Correct
    'autoLog', false,             -- Correct
    'inputSchema', '{}'::jsonb    -- Correct
)
```

### Why This Matters

1. **JSON has its own conventions**: JSON originated from JavaScript, which universally uses camelCase
2. **Ecosystem consistency**: GitHub API, Stripe, OpenAPI, MCP protocol all use camelCase
3. **External consumers**: APIs consumed by JavaScript/TypeScript expect camelCase
4. **Container vs content**: PostgreSQL (container) conventions don't dictate JSON (content) conventions

### Detection

When reviewing code, flag any JSON/JSONB construction with snake_case keys:

```sql
-- Flag these patterns:
jsonb_build_object('snake_case_key', value)
'{"snake_case_key": "value"}'::jsonb
p_metadata->>'snake_case_key'
```

### Standard Handler Metadata Keys (Reference)

All handler registration uses these **camelCase** keys:

| Correct (camelCase) | Wrong (snake_case) |
|---------------------|---------------------|
| `httpMethod` | `http_method` |
| `autoLog` | `auto_log` |
| `responseHeaders` | `response_headers` |
| `methodName` | `method_name` |
| `inputSchema` | `input_schema` |
| `uriTemplate` | `uri_template` |
| `mimeType` | `mime_type` |
| `isError` | `is_error` |
| `requestId` | `request_id` |

---

## Review Checklist for PostgreSQL Code

### SQL vs PL/pgSQL
- [ ] SQL used for set operations?
- [ ] PL/pgSQL only when control flow / dynamic SQL needed?
- [ ] Functions marked IMMUTABLE/STABLE where applicable?

### Performance
- [ ] Set operations preferred over loops?
- [ ] CTEs used appropriately (readability vs optimization)?
- [ ] Index-friendly predicates?
- [ ] LATERAL for correlated subqueries?

### Security
- [ ] RLS enabled on multi-tenant tables?
- [ ] Column-level grants for sensitive data?
- [ ] Encryption for PII/credentials?
- [ ] SQL injection prevention (format %I/%L)?
- [ ] Principle of least privilege?

### Enterprise Quality
- [ ] Audit trail for deployments?
- [ ] Idempotent DDL (IF EXISTS)?
- [ ] Rollback strategy documented?
- [ ] Compliance requirements considered?

### pgmi Philosophy
- [ ] Uses planning helper functions?
- [ ] Dollar-quoting for blocks?
- [ ] Session-scoped temp tables?
- [ ] Fail-fast with RAISE EXCEPTION?

### JSON/JSONB Conventions
- [ ] All JSON keys use camelCase (not snake_case)?
- [ ] Handler metadata keys follow standard naming (httpMethod, autoLog, inputSchema)?
- [ ] JSON content follows JSON conventions, not PostgreSQL conventions?

---

**End of pgmi-postgres-review**

