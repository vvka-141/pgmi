---
name: postgresql-patterns
description: "Use when working with EXECUTE, format(), composite types, or dynamic SQL in PostgreSQL"
user_invocable: true
---


**Purpose**: Quick reference for common PostgreSQL patterns, gotchas, and solutions.

**Target Audience**: Developers working with PL/pgSQL, dynamic SQL, and advanced PostgreSQL features.

---

## Dynamic SQL with EXECUTE

### Pattern: EXECUTE...INTO with Composite Type Returns

**Problem**: Function returns composite type, but EXECUTE...INTO tries to destructure it incorrectly.

**Symptom**: `ERROR: invalid input syntax for type <field_type>: "(field1, field2, field3)"`

**Root Cause**: PostgreSQL represents composite types as tuples when converting to text. EXECUTE with `SELECT function()` tries to assign this text representation to the first field of the target variable.

**❌ Anti-Pattern**:
```sql
DECLARE
    v_result my_composite_type;
    v_sql text;
BEGIN
    v_sql := 'SELECT my_function($1)';
    EXECUTE v_sql INTO v_result USING some_param;
    -- ERROR: tries to parse "(val1, val2, val3)" as first field type
END;
```

**✅ Solution 1** (Recommended): Use `SELECT * FROM function()`:
```sql
DECLARE
    v_result my_composite_type;
    v_sql text;
BEGIN
    v_sql := 'SELECT * FROM my_function($1)';
    EXECUTE v_sql INTO v_result USING some_param;
    -- Success: properly destructures composite
END;
```

**✅ Solution 2**: Use ROW constructor:
```sql
DECLARE
    v_result my_composite_type;
    v_sql text;
BEGIN
    v_sql := 'SELECT ROW(my_function($1))';
    EXECUTE v_sql INTO v_result USING some_param;
END;
```

**When to Use**:
- Function returns custom composite type
- Using EXECUTE for dynamic function calls
- INTO clause expects composite type

**Reference**: PostgreSQL docs on EXECUTE statement, composite types

---

## Type Handling in format()

### Pattern: format() Placeholders

**%I (Identifier)**: For table names, column names, schema names
**%L (Literal)**: For string literals (auto-quotes and escapes)
**%s (String)**: Direct substitution (use carefully)

**❌ Anti-Pattern**: Using %I for type names:
```sql
format('SELECT ($1::%I)', 'api.rest_request')
-- Produces: SELECT ($1::"api.rest_request")
-- Error: type "api.rest_request" does not exist
```

**Why**: `%I` quotes identifiers, treating them as table/column names. Type names in PostgreSQL are not quoted.

**✅ Solution**: Hardcode type names or validate separately:
```sql
format('SELECT ($1::api.rest_request)')
-- Produces: SELECT ($1::api.rest_request)
```

**When Type Names Must Be Dynamic**:
```sql
-- Validate type exists first
IF NOT EXISTS (
    SELECT 1 FROM pg_type WHERE typname = p_type_name
) THEN
    RAISE EXCEPTION 'Invalid type: %', p_type_name;
END IF;

-- Then use direct substitution (validated input)
v_sql := format('SELECT ($1::%s)', p_type_name);
```

---

## Composite Types vs RECORD

### When to Use Each

**Composite Type** (Defined type):
```sql
CREATE TYPE api.http_response AS (
    status_code integer,
    headers extensions.hstore,
    content jsonb
);
```

**Use when**:
- Type has fixed structure
- Multiple functions return same type
- Type definition documents the interface
- Performance matters (compiled type)

**RECORD Type** (Generic):
```sql
DECLARE
    v_result record;
BEGIN
    SELECT * INTO v_result FROM some_table WHERE id = 1;
END;
```

**Use when**:
- Structure varies dynamically
- One-off return from query
- Prototyping/debugging

**❌ Don't Mix**: Don't try to assign composite type to RECORD in EXECUTE:
```sql
-- Anti-pattern
DECLARE
    v_result record;
BEGIN
    EXECUTE 'SELECT my_composite_function()' INTO v_result;
    -- May work but loses type information
END;
```

---

## Quoting and SQL Injection

### Pattern: Safe Dynamic SQL

**❌ Anti-Pattern**: String concatenation:
```sql
v_sql := 'SELECT * FROM ' || table_name || ' WHERE id = ' || user_id;
EXECUTE v_sql;
-- SQL injection vulnerability!
```

**✅ Solution**: Use format() with proper placeholders + USING:
```sql
v_sql := format('SELECT * FROM %I WHERE id = $1', table_name);
EXECUTE v_sql USING user_id;
-- Safe: %I quotes identifier, $1 parameterizes value
```

**Format Placeholder Rules**:
- `%I` → Identifiers (tables, columns, schemas) - SAFE
- `%L` → Literals (strings, numbers) - SAFE (quotes and escapes)
- `%s` → Direct substitution - DANGEROUS (only use with validated input)

**USING Clause**:
- Always use for user input
- Prevents SQL injection
- Handles type conversion
- Better performance (plan caching)

---

## Transaction Control in Functions

### Pattern: Exception Handling with Savepoints

**In pgmi context**: Tests run in savepoints that roll back automatically.

**❌ Anti-Pattern**: Explicit savepoints in test code:
```sql
-- Don't do this in tests
SAVEPOINT my_test;
-- ... test logic ...
ROLLBACK TO my_test;
-- pgmi already provides this via test harness
```

**✅ Pattern**: Let pgmi manage savepoints:
```sql
-- In test file
DO $$
BEGIN
    -- Test logic here
    IF condition THEN
        RAISE EXCEPTION 'TEST FAILED: ...';
    END IF;
END $$;
-- pgmi wraps this in SAVEPOINT automatically
```

**For production code**: Use exception blocks:
```sql
BEGIN
    -- Attempt operation
    INSERT INTO users VALUES (...);
EXCEPTION
    WHEN unique_violation THEN
        -- Handle specific error
        RAISE NOTICE 'User already exists';
        RETURN NULL;
    WHEN OTHERS THEN
        -- Re-raise unexpected errors
        RAISE;
END;
```

---

## Dollar Quoting

### Pattern: Nested String Literals

**Problem**: Embedding SQL inside PL/pgSQL requires escaping quotes.

**❌ Anti-Pattern**: Backslash escaping:
```sql
EXECUTE 'SELECT * FROM users WHERE name = ''John''';
-- Hard to read, error-prone
```

**✅ Solution**: Dollar quoting with unique tags:
```sql
EXECUTE $$
    SELECT * FROM users WHERE name = 'John'
$$;

-- For nested dollar quotes, use unique tags
v_function_body := $outer$
    BEGIN
        EXECUTE $inner$
            SELECT * FROM users WHERE name = 'John'
        $inner$;
    END
$outer$;
```

**Tag Naming Conventions**:
- `$$` - Outermost level
- `$body$` - Function body
- `$sql$` - SQL statement inside function
- `$test$` - Test code

---

## Type Casting Patterns

### Pattern: Safe Type Conversion

**❌ Anti-Pattern**: Direct casting (throws on invalid input):
```sql
SELECT '2024-13-45'::date;
-- ERROR: date/time field value out of range
```

**✅ Solution 1**: Try-catch in PL/pgSQL:
```sql
CREATE FUNCTION safe_cast_date(p_input text) RETURNS date AS $$
BEGIN
    RETURN p_input::date;
EXCEPTION
    WHEN OTHERS THEN
        RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE STRICT;
```

**✅ Solution 2**: Use pgmi's try_cast utilities:
```sql
-- Uses ?> operator for fallback
SELECT '2024-13-45' ?> '2024-01-01'::date;
-- Returns: 2024-01-01 (fallback)
```

**In pgmi templates**: See `common/cast.sql` for comprehensive try-cast functions.

---

## Function Volatility

### Pattern: Correct Volatility Marking

**IMMUTABLE**: Function always returns same result for same input
```sql
CREATE FUNCTION add(a int, b int) RETURNS int AS $$
    SELECT a + b;
$$ LANGUAGE sql IMMUTABLE;
-- Safe: mathematical operation
```

**STABLE**: Function returns same result within single query
```sql
CREATE FUNCTION current_user_id() RETURNS uuid AS $$
    SELECT user_id FROM session_context;
$$ LANGUAGE sql STABLE;
-- Safe: stable within transaction
```

**VOLATILE** (default): Function may have side effects
```sql
CREATE FUNCTION log_access() RETURNS void AS $$
    INSERT INTO access_log VALUES (now(), current_user);
$$ LANGUAGE sql VOLATILE;
-- Correct: has side effects
```

**❌ Common Mistake**: Marking function IMMUTABLE when it uses `now()`:
```sql
-- WRONG!
CREATE FUNCTION with_timestamp() RETURNS timestamptz AS $$
    SELECT now();
$$ LANGUAGE sql IMMUTABLE;
-- now() changes between calls!
```

**Impact**: Incorrect volatility can cause:
- Index scans to be skipped
- Inlining when shouldn't
- Results to be cached incorrectly

---

## RAISE Levels

### Pattern: Appropriate Logging

**NOTICE**: Informational (default)
```sql
RAISE NOTICE 'Processing record: %', record_id;
```

**WARNING**: Something unusual but not fatal
```sql
RAISE WARNING 'Deprecated parameter used: %', param_name;
```

**EXCEPTION**: Fatal error (stops execution)
```sql
RAISE EXCEPTION 'TEST FAILED: Expected %, got %', expected, actual;
```

**DEBUG**: Verbose logging (usually filtered)
```sql
RAISE DEBUG 'Variable state: %', v_debug_info;
```

**In pgmi**:
- Tests use `RAISE EXCEPTION` for failures
- Framework uses `RAISE NOTICE` for progress
- Avoid `RAISE WARNING` in production (clutters logs)

---

## Performance Patterns

### Pattern: Avoiding Row-by-Row Processing

**❌ Anti-Pattern**: Loop over rows:
```sql
FOR v_record IN SELECT * FROM users LOOP
    UPDATE users SET last_seen = now() WHERE id = v_record.id;
END LOOP;
-- Slow: N queries
```

**✅ Solution**: Set-based operation:
```sql
UPDATE users SET last_seen = now();
-- Fast: 1 query
```

**When Loops Are Necessary**: Use bulk operations:
```sql
-- Collect IDs first
SELECT array_agg(id) INTO v_ids FROM users WHERE active;

-- Bulk operation
UPDATE users SET last_seen = now() WHERE id = ANY(v_ids);
```

---

## Common SQLSTATE Codes

Quick reference for error interpretation:

| Code | Meaning | Common Cause |
|------|---------|--------------|
| `22P02` | Invalid text representation | Type conversion failure |
| `23505` | Unique violation | Duplicate key |
| `23503` | Foreign key violation | Referenced row doesn't exist |
| `42P01` | Undefined table | Table doesn't exist or wrong schema |
| `42883` | Undefined function | Function doesn't exist or wrong signature |
| `42703` | Undefined column | Column doesn't exist |
| `42804` | Datatype mismatch | Incompatible types |
| `P0001` | RAISE EXCEPTION | User-defined exception |

**In pgmi**: Use `api.classify_sqlstate()` to get error classification.

---

## SQL Coding Standards

### Table Alias Convention: `c_` Prefix

Use `c_[table_name]` as table aliases for consistency and self-documentation.

```sql
-- ❌ BAD: Cryptic single-letter aliases
SELECT o.title, u.name, m.role
FROM membership.organization o
INNER JOIN membership.user u ON u.object_id = o.owner_object_id
INNER JOIN membership.organization_member m ON m.user_object_id = u.object_id;

-- ✅ GOOD: Self-documenting c_ prefix
SELECT c_organization.title, c_user.name, c_member.role
FROM membership.organization c_organization
INNER JOIN membership.user c_user ON c_user.object_id = c_organization.owner_object_id
INNER JOIN membership.organization_member c_member
    ON c_member.user_object_id = c_user.object_id;
```

**Collisions** (same table twice): Use descriptive suffixes:
```sql
SELECT c_user_owner.name, c_user_manager.name
FROM membership.user c_user_owner
INNER JOIN membership.user c_user_manager ON c_user_manager.object_id = c_user_owner.manager_object_id;
```

**CTEs**: Same convention:
```sql
WITH c_active_projects AS (
    SELECT object_id, name FROM project_design.project WHERE is_deleted = false
)
SELECT c_active_projects.name FROM c_active_projects;
```

---

### Explicit JOIN Types

Always use explicit JOIN keywords — never bare `JOIN`.

```sql
-- ❌ BAD: Implicit join type
SELECT * FROM users u
JOIN orders o ON o.user_id = u.id;

-- ✅ GOOD: Explicit
SELECT * FROM users u
INNER JOIN orders o ON o.user_id = u.id;
```

Use `INNER JOIN`, `LEFT JOIN`, `RIGHT JOIN`, `CROSS JOIN` explicitly.

---

### Inline Table Constraints

Define constraints inline in CREATE TABLE, not via separate ALTER TABLE statements.

```sql
-- ❌ BAD: Separate ALTER TABLE for constraints
CREATE TABLE project_design.activity (
    project_id uuid NOT NULL,
    name text NOT NULL
);
ALTER TABLE project_design.activity ADD PRIMARY KEY (object_id);
ALTER TABLE project_design.activity ADD FOREIGN KEY (project_id) REFERENCES ...;

-- ✅ GOOD: Inline constraints
CREATE TABLE project_design.activity (
    project_object_id uuid NOT NULL REFERENCES project_design.project(object_id) ON DELETE CASCADE,
    name text NOT NULL,
    PRIMARY KEY (object_id),
    CONSTRAINT activity_name_not_empty CHECK (char_length(trim(name)) > 0)
);
```

---

### UNNEST + IN for Array Membership

When checking if a value exists in an array from a subquery, use `IN (SELECT unnest(...))`, not `= ANY((SELECT ...))`.

```sql
-- ❌ WRONG: Fails with "operator does not exist: uuid = uuid[]"
CREATE POLICY org_select ON membership.organization
    FOR SELECT TO customer
    USING (object_id = ANY(
        SELECT c.member_org_ids  -- Returns uuid[], not multiple rows
        FROM membership_claims c
        WHERE c.subject_id = current_setting('auth.subject')
    ));

-- ✅ CORRECT: UNNEST expands array to rows
CREATE POLICY org_select ON membership.organization
    FOR SELECT TO customer
    USING (object_id IN (
        SELECT unnest(c.member_org_ids)
        FROM membership_claims c
        WHERE c.subject_id = current_setting('auth.subject')
    ));
```

COALESCE is unnecessary: if the subquery returns no rows or the array is empty, `unnest` returns an empty set, and `IN (empty_set)` evaluates to FALSE.

---

### Race Condition Handling with ON CONFLICT

Use `ON CONFLICT DO NOTHING` + `FOUND` for concurrent insert scenarios.

```sql
INSERT INTO user_identity (user_object_id, provider, subject_id)
VALUES (v_user.object_id, p_provider, p_subject_id)
ON CONFLICT (provider, subject_id) DO NOTHING;

IF NOT FOUND THEN
    -- Another request won the race — clean up orphan, return winner's record
    DELETE FROM membership.user WHERE object_id = v_user.object_id;
    SELECT * INTO v_user FROM membership.user u
    INNER JOIN user_identity i ON i.user_object_id = u.object_id
    WHERE i.provider = p_provider AND i.subject_id = p_subject_id;
    RETURN v_user;
END IF;
```

Key points:
- `ON CONFLICT DO NOTHING` prevents constraint violation errors
- `FOUND` is FALSE when insert was skipped due to conflict
- Always clean up orphan records created before the conflict

---

### DEBUG vs NOTICE Notification Levels

Use `RAISE DEBUG` for routine operations; reserve `RAISE NOTICE` for genuinely informative messages.

**DEBUG** (hidden by default, visible with `pgmi --verbose`):
- Deployment/installation logging (handler registration, function creation)
- Test assertion progress
- Routine operation progress
- API inventory listings

**NOTICE** (always visible):
- Meaningful warnings (approaching limits, deprecated usage)
- Important state changes (user provisioning, organization creation)
- Error context to help diagnose failures
- Significant events (first-time setup)

```sql
-- ❌ BAD: Verbose NOTICE for routine output
RAISE NOTICE '-> Installing REST handlers';
RAISE NOTICE '  + GET /projects';

-- ✅ GOOD: DEBUG for routine, NOTICE for meaningful events
RAISE DEBUG '-> Installing REST handlers';
RAISE DEBUG '  + GET /projects';
RAISE NOTICE 'Created organization "%" for user %', v_title, v_email;
```

Test files should use DEBUG for assertion progress to avoid cluttering deployment output.

---

### No-Migration Patterns

Write clean, direct SQL. Use IF EXISTS/IF NOT EXISTS only for idempotent re-execution, not for schema evolution.

**Acceptable IF EXISTS usage**:
- Role creation: `IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ...)`
- RLS policy recreation: `DROP POLICY IF EXISTS ... CREATE POLICY ...`
- Extension creation: `CREATE EXTENSION IF NOT EXISTS ...`

**Not acceptable** (migration-style conditionals):
```sql
-- ❌ BAD: Migration-style column check
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid = 'my_table'::regclass AND attname = 'new_column') THEN
        ALTER TABLE my_table ADD COLUMN new_column text;
    END IF;
END $$;
```

No users = no data to migrate. pgmi's `--force` flag handles schema recreation.

---

## Summary: Quick Decision Tree

**Dynamic SQL needed?**
- Yes → Use EXECUTE with format() and USING
  - Identifiers → `%I`
  - Literals → `%L`
  - User input → USING clause

**Function returns composite?**
- Yes + EXECUTE → Use `SELECT * FROM function()`

**Type name in query?**
- Hardcode if possible
- If dynamic → Validate first, then `%s`

**Iteration needed?**
- Try set-based first
- If impossible → Bulk operations

**Error handling?**
- Tests → `RAISE EXCEPTION`
- Production → EXCEPTION blocks with specific WHEN clauses

---

**Last Updated**: 2025-11-22
**References**: PostgreSQL 16 Documentation, pgmi codebase patterns

