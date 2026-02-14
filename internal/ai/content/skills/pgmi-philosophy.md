---
name: pgmi-philosophy
description: "Architectural decisions, execution fabric vs migration framework"
user_invocable: false
---

## Purpose

Deep understanding of pgmi's execution fabric philosophy and design constraints to ensure changes align with the project's foundational principles and architectural boundaries.

## When to Use

- ✅ When planning any pgmi feature or enhancement
- ✅ When evaluating CLI flag additions
- ✅ When modifying deployment orchestration
- ✅ When designing new templates
- ❌ For general PostgreSQL projects (this is pgmi-specific)

## Core Identity

**pgmi is an execution fabric, NOT a migration framework**

This distinction is fundamental to all design decisions:

### What pgmi DOES (Execution Fabric)
- Connect to PostgreSQL using specified credentials
- Prepare session-scoped temp tables with files and parameters
- Execute `deploy.sql` which orchestrates deployment via `pg_temp.pgmi_plan_view`
- Provide CLI for connection/parameter management

### What pgmi NEVER Does (User's Domain)
- Decide transaction boundaries (BEGIN/COMMIT belong in deploy.sql)
- Control execution order (deploy.sql builds the plan)
- Implement retry logic (user handles in SQL via EXCEPTION blocks)
- Enforce idempotency patterns (user's responsibility in SQL)
- Manage locking strategy (PostgreSQL's native behavior prevails)
- Make any "how to deploy" decisions

**Tagline**: A PostgreSQL-native execution fabric for humans and autonomous agents—deterministic, auditable, and transactionally safe.

## Architectural Foundations

### 1. Session-Centric Model

**All deployment work happens in a single PostgreSQL session**

**Workflow**:
```
1. pgmi connects to PostgreSQL
2. Creates session-scoped temporary tables, views, and helper functions:
   - pg_temp._pgmi_source (SQL files and metadata)
   - pg_temp.pgmi_plan_view (VIEW ordering files for execution)
   - pg_temp.pgmi_parameter_view (CLI-supplied parameters)
   - pg_temp.pgmi_test_source_view (test files isolated from deployment)
   - Parameters accessible via current_setting('pgmi.key', true)
   - Helper functions: pgmi_test_plan(), pgmi_test_generate()
3. Executes deploy.sql, which queries files and executes them directly
```

**Key Principle**: Session scope ensures clean separation—no persistent metadata pollution, session ends and everything disappears.

### 2. SQL-Centric Control

**Users control EVERYTHING through SQL**

- **Execution order**: deploy.sql decides what runs when via `pgmi_plan_view ORDER BY execution_order`
- **Transaction boundaries**: BEGIN/COMMIT in deploy.sql, not CLI flags
- **Error handling**: EXCEPTION blocks in SQL, not retry logic in Go
- **Idempotency**: User's responsibility via CREATE OR REPLACE, IF NOT EXISTS, etc.
- **Locking**: PostgreSQL's native behavior, controlled via SQL

**Philosophy**: pgmi feels like a native PostgreSQL extension, not an abstraction layer.

**Example**:
```sql
-- deploy.sql controls everything with direct execution
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    -- User controls execution order via query
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

### 3. Parameter System: Session Configuration Variables

**Automatic Initialization**:
- CLI parameters (`--param key=value`) automatically become session configuration variables
- No manual initialization needed - parameters are immediately accessible
- Session variables use `pgmi.` namespace prefix
- Accessible via `current_setting('pgmi.key', true)` with COALESCE for defaults

**Parameter Access**:
Use PostgreSQL's native `current_setting()` function:
- `COALESCE(current_setting('pgmi.key', true), 'default')` for optional parameters
- `current_setting('pgmi.key')` for required parameters (fails if not set)

**Template-Specific Helpers**:
Templates can define their own helper functions for parameter access with defaults, validation, and type coercion (e.g., `deployment_setting()` in the advanced template).

**Example**:
```sql
-- Parameters are immediately accessible
DO $$
DECLARE
    v_env TEXT := COALESCE(current_setting('pgmi.env', true), 'development');
BEGIN
    IF v_env = 'production' THEN
        RAISE NOTICE 'Production deployment detected';
    END IF;
END $$;
```

**Security Note**: Never pass secrets as parameters (passwords, API keys, tokens). Use PostgreSQL connection strings or environment variables.

### 4. Dual Checksum Strategy

**Normalized Checksum** (content identity):
- Converts content to lowercase
- Removes all SQL comments (`--` and `/* */`)
- Collapses whitespace sequences to single spaces
- Enables tracking scripts independent of formatting changes

**Raw Checksum** (exact change detection):
- Hash of the original file content without modifications
- Detects any changes including whitespace and comments

**Execution Principle**: Execution order is defined by deploy.sql (typically phased directories + lexicographic path). Checksums provide content identity for idempotency and rename detection; they do not define order.

## CLI Design Philosophy

**Scope of CLI Flags**: Infrastructure concerns only, never deployment orchestration

### ✓ Valid CLI Concerns

**Connection Parameters**:
- `--connection`, `--host`, `--port`, `--username`, `--database`
- Authentication credentials (via connection string or environment variables)

**Parameter Injection**:
- `--param`, `--params-file`

**Safety Workflows**:
- `--overwrite`, `--force` for destructive operations
- Approver interface for user confirmation workflows

**Observability**:
- `--verbose`, `--json` output

**Catastrophic Failure Protection**:
- `--timeout` as safety net (default: 3 minutes)

### ✗ Invalid CLI Concerns (Belong in deploy.sql)

**Transaction Control**:
- ❌ `--no-transaction`, `--single-transaction`
- ✅ Use BEGIN/COMMIT in deploy.sql

**Phase Skipping**:
- ❌ `--skip-migrations`, `--skip-tests`
- ✅ Control execution in deploy.sql logic

**Execution Order Control**:
- ❌ `--reverse`, `--from-migration`
- ✅ Build plan in deploy.sql

**Retry Behavior**:
- ❌ `--retry-count`, `--retry-delay`
- ✅ Implement in SQL via EXCEPTION blocks

**Idempotency Enforcement**:
- ❌ `--force-rerun`, `--skip-if-exists`
- ✅ User's responsibility in SQL

**The Two-Database Pattern**:

The `-d/--database` flag specifies the **deployment target** (database to create/deploy to), while the connection string specifies the **maintenance database** (where to connect to run CREATE DATABASE):

```bash
# Connect to 'postgres' (maintenance DB), deploy to 'myapp' (target DB)
pgmi deploy ./migrations -d myapp

# Explicit maintenance DB in connection string
pgmi deploy ./migrations \
  --connection "postgresql://user@host/postgres" \
  -d myapp
```

This separation is intentional: CREATE DATABASE must run from a maintenance database, not the target database being created.

**Timeout Flag Design**:

The `--timeout` flag is a **catastrophic failure protection mechanism**, not a deployment control:
- Purpose: Prevent indefinite hangs due to network issues, deadlocks, or runaway queries
- Default is short (3m) by design: forces developers to notice deployment issues quickly
- DevOps/production: Teams should explicitly set timeouts based on their deployment characteristics
- PostgreSQL's native timeouts (statement_timeout, lock_timeout) remain the primary timeout mechanism
- Users control timeout behavior in deploy.sql using PostgreSQL's SET commands

## Operational Modes & Transaction Strategies

**Single-transaction mode**:
- Orchestrate "all-or-nothing" release inside deploy.sql with one large transaction
- Provides maximum atomicity but longest lock duration

**Phased commits**:
- Commit per phase (e.g., pre-deployment → migrations → setup → post-deployment → tests)
- Limits lock scope and allows partial progress
- Reduces contention on high-traffic objects

**Locking Posture**:
- pgmi does not alter PostgreSQL's native locking behavior
- Lock scope and duration follow your chosen transaction strategy
- Prefer phased commits for long-running DDL
- Use project conventions (pre/migrate/setup phases) to isolate heavyweight DDL from lightweight idempotent setup

## AI Integration & Observability

pgmi is designed to be exceptionally legible to autonomous agents and GenAI code systems:

**Stable Contract**:
- Single-session model with deterministic inputs (folder path + connection + params)
- Order defined by deploy.sql, content identity via checksums
- Real-time progress via PostgreSQL's `RAISE NOTICE` mechanism
- Consistent CLI exit codes (0 = success, non-zero = categorized failure)

**AI-Friendly Features**:
- SQL-centric: no proprietary framework knowledge required
- Transactional robustness: clear success/rollback semantics
- Observability: all progress comes from SQL scripts via `RAISE NOTICE`
- Deterministic rendering (roadmap): placeholder resolution with validation

## SQL Coding Standards

### Dollar-Quoting for String Literals

**Always use PostgreSQL dollar-quoting (`$$`) syntax** for multi-line string literals and embedded SQL code.

**Benefits**:
1. Better IDE Support: Syntax highlighting works correctly
2. No Escaping Needed: Eliminates complex quote escaping (`''`)
3. Readability: Code is much cleaner and easier to maintain
4. Consistency: Follows PostgreSQL best practices

**Examples**:
```sql
-- ❌ BAD: Using single quotes with escaping
EXECUTE format('SELECT ''Hello %s''', v_name);

-- ✅ GOOD: Using dollar-quoting
EXECUTE format($sql$SELECT 'Hello %s'$sql$, v_name);

-- For nested dollar-quotes, use labeled delimiters
DO $outer$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (SELECT content FROM pg_temp.pgmi_plan_view) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $outer$;
```

### Session API Reference

**`pgmi_plan_view`** - VIEW ordering files for execution:
```sql
-- Query files in execution order and execute directly
FOR v_file IN (
    SELECT path, content FROM pg_temp.pgmi_plan_view
    ORDER BY execution_order
)
LOOP
    EXECUTE v_file.content;
END LOOP;
```

**Parameter Access** - Use PostgreSQL's native `current_setting()` with COALESCE:
```sql
v_env := COALESCE(current_setting('pgmi.env', true), 'development');
```

**`pgmi_test_plan(pattern)`** - Returns test execution plan (TABLE function):
```sql
-- Query test plan
SELECT * FROM pg_temp.pgmi_test_plan();

-- With pattern filter
SELECT * FROM pg_temp.pgmi_test_plan('.*/api/.*');
```

**`pgmi_test()` macro** - Run tests with automatic savepoints (preprocessor macro):
```sql
-- Run all tests
pgmi_test();

-- Run filtered tests
pgmi_test('.*/pre-deployment/.*');
```

## Design Heuristics

### When Evaluating New Features

**Ask these questions**:
1. **Is this infrastructure or orchestration?**
   - Infrastructure → CLI/Go code
   - Orchestration → deploy.sql/SQL

2. **Does this violate SQL-centric control?**
   - If CLI takes control away from SQL → reject

3. **Could this be implemented in SQL instead?**
   - If yes → prefer SQL implementation

4. **Does this add persistent state?**
   - If yes, ensure it's truly necessary (prefer session-scoped)

5. **Is this consistent with "feels like PostgreSQL extension"?**
   - If requires learning new concepts → reconsider

### When Designing CLI Flags

**Checklist**:
- [ ] Is this about "what to connect to"? (valid)
- [ ] Is this about "what parameters to pass"? (valid)
- [ ] Is this about "how to deploy"? (INVALID - belongs in deploy.sql)
- [ ] Does this control execution flow? (INVALID - belongs in deploy.sql)
- [ ] Is this a safety mechanism? (valid if infrastructure-level)

## Integration with Other Skills

- **Guides**: All pgmi-related planning and implementation
- **Required for**: pgmi-test-architecture.md, pgmi-metadata-system.md
- **Informs**: architecture-alignment.md (pgmi-specific patterns)
- **Combines with**: decision-framework.md (evaluate against philosophy)

## Common Pitfalls

- ❌ **Adding Orchestration to CLI**: CLI flags that control "how to deploy"
- ✅ **Keep CLI Infrastructure-Only**: Only connection, parameters, safety

- ❌ **Persistent Metadata**: Storing deployment state in permanent tables
- ✅ **Session-Scoped**: Use pg_temp tables, clean session separation

- ❌ **Go Code Orchestration**: Business logic in Go for deployment flow
- ✅ **SQL Orchestration**: User controls everything via deploy.sql

- ❌ **Abstracting PostgreSQL**: Hiding native behavior behind framework
- ✅ **Native PostgreSQL**: Expose PostgreSQL's full power to user

## Examples

### Example 1: Feature Request - Skip Migrations Flag

**Request**: Add `--skip-migrations` CLI flag

**Analysis**:
- Is this infrastructure or orchestration? **Orchestration** (controls what runs)
- Does this violate SQL-centric control? **Yes** (takes control from deploy.sql)
- Could this be implemented in SQL? **Yes** (conditional logic in deploy.sql)

**Decision**: ❌ Reject

**Alternative**:
```sql
-- deploy.sql controls this via parameter
BEGIN;

DO $$
DECLARE
    v_file RECORD;
BEGIN
    IF COALESCE(current_setting('pgmi.skip_migrations', true), 'false') = 'false' THEN
        RAISE NOTICE 'Running migrations...';
        FOR v_file IN (
            SELECT path, content FROM pg_temp.pgmi_plan_view
            WHERE path LIKE './migrations/%'
            ORDER BY execution_order
        )
        LOOP
            RAISE NOTICE 'Executing: %', v_file.path;
            EXECUTE v_file.content;
        END LOOP;
    ELSE
        RAISE NOTICE 'Skipping migrations (skip_migrations=true)';
    END IF;
END $$;

COMMIT;
```

**Usage**: `pgmi deploy . -d mydb --param skip_migrations=true`

### Example 2: Feature Request - Transaction Timeout

**Request**: Add `--transaction-timeout` CLI flag

**Analysis**:
- Is this infrastructure or orchestration? **Orchestration** (controls SQL execution)
- Could this be implemented in SQL? **Yes** (SET statement_timeout)

**Decision**: ❌ Reject CLI flag, ✅ Accept `--timeout` for catastrophic protection

**Implementation**:
```sql
-- deploy.sql controls transaction timeout
SET statement_timeout = '5min';

BEGIN;
-- ... deployment work ...
COMMIT;

-- Or per-phase timeouts
SET statement_timeout = '1min';
-- Fast operations
SET statement_timeout = '30min';
-- Slow migrations
```

**Catastrophic protection**: `pgmi deploy . -d mydb --timeout 60m` (safety net only)

