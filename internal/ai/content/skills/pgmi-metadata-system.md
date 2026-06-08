---
name: pgmi-metadata-system
description: "Use when working with <pgmi-meta> blocks, sortKeys, or execution ordering in advanced templates"
user_invocable: true
---


## Purpose

Comprehensive understanding of pgmi's optional metadata system using XML blocks embedded in SQL comments, enabling path-independent script tracking, multi-phase execution, and explicit execution ordering.

## When to Use

- ✅ When planning advanced template projects (metadata REQUIRED)
- ✅ When scripts need path-independent identity (track by UUID)
- ✅ When multi-phase execution needed (same script at multiple stages)
- ✅ When explicit execution ordering required (override lexicographic)
- ❌ For simple linear migrations (use basic template without metadata)
- ❌ For test files (`__test__/` directories - metadata skipped)

## Core Design Principles

**Optional**: Files without metadata still work (use deterministic fallback UUID)

**Fail-Fast**: Invalid metadata caught during file scanning (before DB session)

**Pure SQL**: Execution order computed in PostgreSQL via simple UNNEST and ORDER BY

**Simplified**: No dependency graphs, no topological sort, no groups - just explicit ordering

## Metadata System Overview

### When to Use Metadata

**✅ Use Metadata For**:
- Advanced template projects (metadata REQUIRED)
- Scripts that need path-independent identity (survives renames/moves)
- Multi-phase execution (same script runs at multiple deployment stages)
- Explicit execution ordering (override lexicographic path order)
- Idempotency control (one-time vs always-rerun)

**❌ Don't Use Metadata For**:
- Simple linear migrations (basic template sufficient)
- Test files (`__test__/` directories - metadata automatically skipped)
- Prototype/throwaway scripts
- Projects with < 10 SQL files

## XML Schema & Syntax

### Root Element: `<pgmi-meta>`

**Required Attributes**:
- `id` (UUID): Globally unique identifier, RFC 4122 format
- `idempotent` (boolean): `true` = always re-execute, `false` = execute once

**Optional Child Elements**:
- `<description>`: Human-readable explanation
- `<sortKeys>`: Array of sort keys for multi-phase execution

### Complete Example

```sql
/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">

  <description>
    PostgreSQL utility functions for safe type conversions
  </description>

  <sortKeys>
    <key>10-common/0010</key>
  </sortKeys>

</pgmi-meta>
*/

-- Your SQL code follows here...
CREATE OR REPLACE FUNCTION common.try_cast(...)
RETURNS ...
AS $$
BEGIN
    -- Implementation
END;
$$ LANGUAGE plpgsql;
```

### Multi-Phase Execution Example

```sql
/*
<pgmi-meta
    id="7603e3af-b8d9-46a5-8c4c-7f74d39e17f9"
    idempotent="true">

  <description>
    Initialize schema, then populate reference data later
  </description>

  <sortKeys>
    <key>20-internal/0000</key>  <!-- Phase 1: Create schema -->
    <key>50-seed/0010</key>      <!-- Phase 2: Populate data -->
  </sortKeys>

</pgmi-meta>
*/

-- This file executes twice at different deployment stages
```

## Execution Order

Scripts execute in deterministic order:
1. **sort_key** (ASC): Primary ordering - users control execution via sort keys
2. **path** (ASC): Tiebreaker for scripts with same sort key

**No dependency resolution, no topological sort** - execution order is explicit and deterministic.

### How Multi-Phase Works

- Each `<key>` in `<sortKeys>` generates a separate execution entry
- Same file content executes multiple times at different stages
- Implemented via PostgreSQL `UNNEST()` operation

**Example**:
```
File: helpers.sql with sortKeys: ['10-common/0010', '50-seed/0020']

Execution plan:
#1: sort_key='10-common/0010', path='./helpers.sql' (first execution)
#2: sort_key='50-seed/0020', path='./helpers.sql' (second execution)
```

## Sort Key Conventions

### Layer-Based (Recommended)

```
00-bootstrap/0000   # Bootstrap: roles, schemas, extensions
10-common/0010       # Layer 1: Utility functions
10-common/0020       # Layer 1: More utilities
20-internal/0000    # Layer 2: Internal infrastructure
30-core/0000        # Layer 3: Core domain logic
40-api/0010         # Layer 4: API endpoints
50-seed/0000        # Layer 5: Reference data seeding
```

### Date-Based (for migrations)

```
migrations/2025-01-15/001
migrations/2025-01-15/002
migrations/2025-01-16/001
```

### Phase-Based (explicit stages)

```
01-pre-deployment/001
02-migrations/001
03-setup/001
04-post-deployment/001
```

**Key Principle**: Sort keys are lexicographic strings - choose a convention and stick to it.

## Idempotency Control

### `idempotent="true"` (Always Re-execute)

**Use for**:
- Idempotent DDL (CREATE OR REPLACE, CREATE IF NOT EXISTS)
- Configuration updates
- View/function definitions
- Reference data upserts

**Example**:
```sql
/*
<pgmi-meta id="..." idempotent="true">
  <description>API endpoint handlers (always update)</description>
</pgmi-meta>
*/

-- Safe to run multiple times
CREATE OR REPLACE FUNCTION api.get_users()
RETURNS SETOF users
AS $$
    SELECT * FROM users WHERE deleted_at IS NULL;
$$ LANGUAGE sql;
```

### `idempotent="false"` (One-Time Execution)

**Use for**:
- Schema migrations (ALTER TABLE ADD COLUMN)
- Data migrations
- Destructive operations (DROP, TRUNCATE)
- Non-idempotent INSERT statements

**Example**:
```sql
/*
<pgmi-meta id="..." idempotent="false">
  <description>Add email column to users table (one-time migration)</description>
</pgmi-meta>
*/

-- Should only run once
ALTER TABLE users ADD COLUMN email TEXT;
```

### Tracking Behavior

```sql
-- Advanced template's deploy.sql checks execution log
INSERT INTO internal.deployment_script_execution_log(...)
ON CONFLICT (script_id) DO UPDATE SET ...;

-- Execute based on idempotency flag
IF v_exec_log.idempotent OR v_exec_log.executed_at = v_now THEN
    EXECUTE v_script.content;  -- Always execute OR first execution today
ELSE
    RAISE NOTICE '[SKIP] One-time script already executed';
END IF;
```

## Fallback Identity System

**Files without metadata automatically receive deterministic UUID**:
```
UUID v5 = SHA1(namespace_uuid, normalized_path)
```

**Normalization Rules**:
- Lowercase conversion
- Remove leading `./`
- Forward slashes enforced

**Example**:
```
"./migrations/001_users.sql" → uuid_v5(namespace, "migrations/001_users.sql")
"./SETUP/Schema.SQL"         → uuid_v5(namespace, "setup/schema.sql")
```

**Benefits**:
- No metadata required for simple projects
- Consistent identity for files without metadata
- Survives case-only renames

**Limitation**:
- Fallback UUID changes if file path changes
- Use explicit metadata for stability across renames

## Session-Scoped Database Objects

### Metadata Table (Simplified)

```sql
CREATE TEMP TABLE _pgmi_source_metadata (
    path TEXT PRIMARY KEY,
    id UUID NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_keys TEXT[] NOT NULL DEFAULT '{}',  -- Array for multi-phase
    description TEXT
);
```

### Execution Plan View (Simple UNNEST)

```sql
CREATE OR REPLACE TEMP VIEW pgmi_plan_view AS
SELECT
    s.path,
    s.content,
    s.pgmi_checksum AS checksum,
    md5(s.path::bytea)::uuid AS generic_id,
    m.id,  -- NULL for files without metadata
    COALESCE(m.idempotent, true) AS idempotent,
    COALESCE(m.description, '') AS description,
    unnested.sort_key,
    ROW_NUMBER() OVER (ORDER BY unnested.sort_key, s.path) AS execution_order
FROM pg_temp._pgmi_source s
LEFT JOIN pg_temp._pgmi_source_metadata m ON s.path = m.path
CROSS JOIN LATERAL UNNEST(
    COALESCE(NULLIF(m.sort_keys, '{}'), ARRAY[s.path])
) AS unnested(sort_key)
ORDER BY unnested.sort_key, s.path;
```

**Key Columns**:
- `id`: User-provided UUID from metadata (nullable)
- `generic_id`: Deterministic fallback UUID (md5 of path)
- `sort_key`: Expanded from sort_keys array (one row per key)
- `execution_order`: Sequential order based on sort_key + path
- `idempotent`: Controls one-time vs always-rerun behavior

**How UNNEST Works**:
```sql
-- Script with multiple sort keys
sort_keys = ['10-common/0010', '50-seed/0020']

-- UNNEST generates multiple rows:
Row 1: sort_key='10-common/0010', execution_order=5
Row 2: sort_key='50-seed/0020', execution_order=87

-- Same script executes twice at different phases
```

## CLI Commands

### Scaffold Metadata

**Generate metadata blocks for files**:
```bash
# Preview only (print to stdout)
pgmi metadata scaffold ./myproject

# Write metadata blocks to files
pgmi metadata scaffold ./myproject --write

# Generate with idempotent=false default
pgmi metadata scaffold ./myproject --idempotent=false --write
```

**What It Does**:
- Scans SQL files without metadata
- Generates UUID for each file
- Infers sort key from file path
- Inserts metadata block at top of file (with --write)

### Validate Metadata

**Check for errors**:
```bash
# Human-readable output
pgmi metadata validate ./myproject

# JSON output for tooling
pgmi metadata validate ./myproject --json
```

**Validates**:
- XML syntax errors
- XSD constraint violations
- Required attributes (id, idempotent)
- Empty sort keys (whitespace-only not allowed)
- Duplicate metadata blocks within file
- Duplicate IDs across files

### Preview Execution Plan

**See execution order before deploying**:
```bash
# Human-readable table
pgmi metadata plan ./myproject

# JSON output for tooling
pgmi metadata plan ./myproject --json
```

**Shows**:
- Execution order
- Script paths
- Sort keys
- IDs (metadata or fallback)
- Idempotency flag

## Validation & Error Handling

### File-Level Validation (During Scan)

**Checked During File Discovery**:
- XML syntax errors
- XSD constraint violations
- Required attributes (id, idempotent)
- Empty sort keys (whitespace-only not allowed)
- Duplicate metadata blocks

**Example Error**:
```
metadata error in ./migrations/001_users.sql (line 5): id attribute is required

Hint: Each script must have a unique identifier.
  Generate with: uuidgen (Linux/Mac), [guid]::NewGuid() (PowerShell)
```

### Cross-File Validation (Pre-Deployment)

**Checked Before Execution**:
- Duplicate IDs across files

**Example Error**:
```
metadata error: duplicate script ID found

ID: 550e8400-e29b-41d4-a716-446655440000
Files:
  - ./migrations/001_users.sql
  - ./setup/users_setup.sql

Hint: Each script must have a globally unique identifier.
  Generate new UUID for one of these files.
```

### Error Message Format

**Structured, Actionable**:
```
metadata error in <file_path> (line <line_number>): <specific_error>

Hint: <actionable_guidance>
  <example_command_or_fix>
```

## Advanced Template Integration

### Bootstrap Phase

**Create Tracking Table** (illustrative — a *simplified* schema for teaching the
pattern; the advanced template's real `internal.deployment_script_execution_log`
uses `deployment_script_object_id`, `deployment_script_content_checksum`,
`file_path`, mandatory FKs, etc. — see `internal/scaffold/templates/advanced/deploy.sql`):
```sql
-- Simplified illustrative tracking table (not the shipped schema):
CREATE TABLE IF NOT EXISTS example_script_log (
    script_id UUID NOT NULL,
    path TEXT NOT NULL,
    idempotent BOOLEAN NOT NULL,
    sort_key TEXT,
    checksum TEXT,
    execution_order INT NOT NULL,
    executed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT script_id_unique UNIQUE (script_id)
);
```

### Main Execution Loop

```sql
FOR v_script IN (
    SELECT * FROM pg_temp.pgmi_plan_view
    WHERE id IS NOT NULL  -- Only explicit metadata scripts
    ORDER BY execution_order ASC
)
LOOP
    RAISE NOTICE '  → [#%] % (% | sort_key: %)',
        v_script.execution_order::text,
        v_script.path,
        CASE WHEN v_script.idempotent THEN 'idempotent' ELSE 'one-time' END,
        v_script.sort_key;

    -- Execute script with tracking
    PERFORM pg_temp.deploy_script_with_tracking(v_script.id, v_script.sort_key);
END LOOP;
```

### Tracking Function

```sql
CREATE OR REPLACE FUNCTION deploy_script_with_tracking(
    p_script_id UUID,
    p_sort_key TEXT
)
RETURNS void AS $$
DECLARE
    v_script RECORD;
    v_exec_log RECORD;
    v_now TIMESTAMPTZ := NOW();
BEGIN
    -- Get script from plan
    SELECT * INTO v_script
    FROM pg_temp.pgmi_plan_view
    WHERE generic_id = p_script_id AND sort_key = p_sort_key;

    -- Check execution log
    SELECT * INTO v_exec_log
    FROM example_script_log
    WHERE script_id = p_script_id;

    -- Determine if should execute
    IF v_exec_log IS NULL THEN
        -- Never executed, run it
        EXECUTE v_script.content;
    ELSIF v_script.idempotent THEN
        -- Idempotent, always run
        EXECUTE v_script.content;
    ELSE
        -- One-time, already executed
        RAISE NOTICE '[SKIP] One-time script already executed: %', v_script.path;
        RETURN;
    END IF;

    -- Update execution log
    INSERT INTO example_script_log
        (script_id, path, idempotent, sort_key, checksum, execution_order, executed_at)
    VALUES
        (v_script.generic_id, v_script.path, v_script.idempotent,
         v_script.sort_key, v_script.checksum, v_script.execution_order, v_now)
    ON CONFLICT (script_id) DO UPDATE SET
        path = EXCLUDED.path,
        checksum = EXCLUDED.checksum,
        executed_at = EXCLUDED.executed_at;
END;
$$ LANGUAGE plpgsql;
```

## Gotchas & Considerations

### Security

- Never include secrets in metadata (visible in SQL comments)
- Session variables are visible via `SHOW ALL`
- Use connection strings for credentials

### Performance

- Simple UNNEST + ORDER BY is fast (no recursive CTEs)
- Scales linearly with number of scripts
- Multi-phase execution = multiple executions of same content

### Compatibility

- Old format `<pgmi:meta xmlns:pgmi="...">` rejected with migration guidance
- Fallback UUID changes if file path changes (use explicit metadata for stability)

### Testing

- Test files (`__test__/`) automatically excluded from metadata processing
- Inline function tests don't need metadata
- Metadata scaffolding skips test directories

## Planning Metadata-Related Changes

### Questions to Ask

1. **Should scripts be tracked by path or UUID?**
   - UUID = stable across renames
   - Path = simple, no metadata needed

2. **Is idempotency control needed?**
   - true = always run (functions, views)
   - false = one-time (migrations, data changes)

3. **Does execution order matter beyond lexicographic path order?**
   - Yes → use sort keys
   - No → use basic template

4. **Should any scripts execute at multiple deployment phases?**
   - Yes → multi-phase execution with multiple sort keys
   - No → single sort key per script

### Impact Analysis Checklist

When modifying metadata system:
- [ ] Metadata schema changes (XSD, validation rules)
- [ ] Session table schema (`_pgmi_source_metadata`)
- [ ] Execution plan view logic (`pgmi_plan_view`)
- [ ] CLI commands (scaffold, validate, plan)
- [ ] Advanced template's deploy.sql
- [ ] Tracking table schema
- [ ] Error messages and hints
- [ ] Fallback identity generation

### Common Change Patterns

**New Metadata Attribute**:
- Update XSD schema definition
- Update types.go (Go struct)
- Update validator.go (validation logic)
- Update session schema (temp table)

**New Validation Rule**:
- Update validator.go
- Add structured error with hint
- Add test case for validation

**New CLI Command**:
- Add to internal/cli/metadata.go
- Follow existing command patterns
- Update CLI documentation

**Template Changes**:
- Update deploy.sql execution loop
- Update tracking table schema
- Update template SQL files with metadata

### Testing Strategy

**Unit Tests**:
- Metadata extraction from SQL files
- XML parsing and validation
- UUID generation (explicit and fallback)

**Integration Tests**:
- UNNEST-based view generates correct plan
- Multi-phase execution creates multiple entries
- Tracking log correctly records executions

**E2E Tests**:
- Sample projects (basic, advanced)
- Scaffold → validate → deploy workflow
- Error message validation (helpful hints)
- Cross-file validation (duplicate IDs)

## Integration with Other Skills

- **Builds on**: pgmi-philosophy.md (session-centric, SQL-centric)
- **Optional enhancement**: Can be ignored for simple projects
- **Informs**: phased-implementation.md (multi-phase execution)
- **Requires**: architecture-alignment.md (consistency in metadata usage)

## Common Pitfalls

- ❌ **Using Metadata for Everything**: Overcomplicating simple projects
- ✅ **Use When Needed**: Standard template sufficient for most cases

- ❌ **Circular Dependencies**: Sort keys creating dependency cycles
- ✅ **Explicit Order**: Linear execution order, no graph resolution

- ❌ **Forgetting Validation**: Deploying without checking metadata
- ✅ **Validate First**: Run `pgmi metadata validate` before deploy

- ❌ **Inconsistent Sort Keys**: Mixing conventions (layer-based, date-based)
- ✅ **Choose Convention**: Stick to one sort key style

## Examples

### Example 1: Simple Utility Function (Idempotent)

```sql
/*
<pgmi-meta
    id="a1b2c3d4-e5f6-4789-a012-3456789abcdef"
    idempotent="true">

  <description>
    Safe UUID casting utility - always update to latest version
  </description>

  <sortKeys>
    <key>10-common/0010</key>
  </sortKeys>

</pgmi-meta>
*/

-- pgmi's advanced template ships common.try_cast(text, default) as an
-- overload-per-type family. Example: common.try_cast('x', NULL::uuid) returns
-- NULL on invalid input instead of raising. Define your own helper the same
-- way when you need a new target type.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value uuid)
RETURNS uuid
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
BEGIN
    RETURN input::uuid;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN default_value;
END;
$$;
```

**Result**: Executes every deployment, always updates function definition.

### Example 2: One-Time Migration (Non-Idempotent)

```sql
/*
<pgmi-meta
    id="f9e8d7c6-b5a4-4321-9876-543210fedcba"
    idempotent="false">

  <description>
    Add deleted_at column for soft deletes (one-time schema change)
  </description>

  <sortKeys>
    <key>02-migrations/2025-01-15/001</key>
  </sortKeys>

</pgmi-meta>
*/

ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE INDEX idx_users_deleted_at ON users(deleted_at) WHERE deleted_at IS NOT NULL;
```

**Result**: Executes once, skipped on subsequent deployments.

### Example 3: Multi-Phase Execution

```sql
/*
<pgmi-meta
    id="11112222-3333-4444-5555-666677778888"
    idempotent="true">

  <description>
    Create roles schema early, populate role data later
  </description>

  <sortKeys>
    <key>00-bootstrap/0020</key>  <!-- Phase 1: Create schema -->
    <key>50-seed/0010</key>        <!-- Phase 2: Seed data -->
  </sortKeys>

</pgmi-meta>
*/

-- Executes in Phase 1 (bootstrap)
CREATE SCHEMA IF NOT EXISTS roles;

-- Executes in Phase 2 (seed)
INSERT INTO roles.role (name) VALUES ('admin'), ('user')
ON CONFLICT (name) DO NOTHING;
```

**Result**: Executes twice at different deployment stages.

