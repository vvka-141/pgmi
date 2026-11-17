---
title: "API versioning"
description: "Design record for pgmi session API versioning and compatibility guarantees."
weight: 10
---

# Change Request: API Versioning for pgmi Session Interface

> **Status: IMPLEMENTED**
> This design document describes changes that have been completed. The "Current State" section reflects the pre-implementation state for historical context. The "Target State" is now the actual implementation. See [Session API](../session-api.md) for current API documentation.

## Overview

Introduce versioned API contracts for pgmi's session-scoped interface (temp tables, views, functions). This enables:
- Stable view and function names and columns for deploy.sql across pgmi upgrades
- DevOps pipelines pinned to specific API versions
- Internal refactoring freedom for pgmi maintainers

## Motivation

Before this change, deploy.sql scripts referenced internal tables like `pg_temp.pgmi_source` directly. If pgmi changes the table structure, all user deploy.sql scripts break. By introducing versioned views as the public API, pgmi can evolve internally while maintaining backward compatibility.

**Use case:** A CI/CD pipeline uses `pgmi deploy --compat=1`. When a later pgmi changes its internal tables, the pipeline's queries still resolve because every v1 view names its columns. `--compat` does not pin behavior: plan ordering, which files load, and how deploy.sql is sent follow the binary version.

**Pre-1.0 promise (supersedes "backward compatible" below):** before pgmi 1.0 the v1 contract may change in a minor release. Every change is listed under Upgrading in that release's notes, and a snapshot of the session API in the test suite makes each one a deliberate diff. No contract-version bump and no old contract files are kept for these changes.

---

## Design Decisions (Already Made)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| CLI flag name | `--compat` | Industry standard (Kubernetes, REST APIs) |
| View naming | `pgmi_*_view` suffix | Explicit, consistent with existing `pgmi_plan_view` |
| Internal table naming | `_pgmi_*` underscore prefix | Convention: underscore = internal |
| Default version | Latest stable | New users get best experience |
| Deprecation policy | Pre-1.0: may change in a minor release, listed under Upgrading | No version removal; behavior follows the binary version |
| File naming | `api-v1.sql`, `api-v2.sql` | Major versions only, no semver in filenames |
| `pgmi_plan_view` location | Moves to `api-v1.sql` | It's part of the public API, references internal tables |
| Macro code generation | SQL function returns replacement code | pgmi calls `pgmi_test_generate()`, replaces macro with result |

---

## Pre-Implementation State (Historical)

### Internal Tables (in `crates/pgmi-shared/session/schema.sql`)

| Table | Purpose | Used By |
|-------|---------|---------|
| `pgmi_source` | Non-test source files | deploy.sql queries, `pgmi_plan_view` |
| `pgmi_source_metadata` | Parsed `<pgmi-meta>` XML | `pgmi_plan_view` JOIN |
| `pgmi_parameter` | CLI parameters | `current_setting('pgmi.key', true)` |
| `pgmi_test_directory` | Test directory hierarchy | `pgmi_test_plan()` |
| `pgmi_test_source` | Test file content | `pgmi_test_plan()`, `pgmi_test()` macro |

### Existing Views

| View | Purpose |
|------|---------|
| `pgmi_plan_view` | Execution order with sort_key expansion (already follows `_view` convention) |

### Existing Functions (Public API)

| Function | Purpose |
|----------|---------|
| `pgmi_test_plan(pattern)` | Return test execution plan |
| `pgmi_is_sql_file(filename)` | Check SQL file extension |
| `pgmi_register_file(...)` | Internal: pgmi calls it to insert files |
| `pgmi_validate_pattern(pattern)` | Internal: Regex validation |
| `pgmi_has_tests(dir, pattern)` | Internal: Recursive test discovery |
| `pgmi_persist_test_plan(schema, pattern)` | Export test plan to permanent table |

**Note:** Parameter access is via `current_setting('pgmi.key', true)`. Templates handle their own declaration, validation, and defaults.

---

## Implemented State

### File Structure

```
crates/pgmi-shared/session/
  schema.sql          # Internal tables (_pgmi_*) and most functions, always executes first
crates/pgmi-shared/contract/
  api-v1.sql          # V1 views and pgmi_test_generate
  api-v2.sql          # Future: V2 contract (when needed)
```

### Renamed Internal Tables (in `schema.sql`)

| Current Name | New Name |
|--------------|----------|
| `pgmi_source` | `_pgmi_source` |
| `pgmi_source_metadata` | `_pgmi_source_metadata` |
| `pgmi_parameter` | `_pgmi_parameter` |
| `pgmi_test_directory` | `_pgmi_test_directory` |
| `pgmi_test_source` | `_pgmi_test_source` |

### V1 API Views (in `api-v1.sql`)

| View | Definition | Notes |
|------|------------|-------|
| `pgmi_source_view` | `SELECT * FROM _pgmi_source` | Primary file access |
| `pgmi_parameter_view` | `SELECT * FROM _pgmi_parameter` | Parameter inspection |
| `pgmi_plan_view` | Complex view with UNNEST/JOIN over `_pgmi_source`, `_pgmi_source_metadata` | Execution ordering (moved from schema.sql) |
| `pgmi_test_source_view` | `SELECT * FROM _pgmi_test_source` | Test file access |
| `pgmi_test_directory_view` | `SELECT * FROM _pgmi_test_directory` | Test hierarchy |
| `pgmi_source_metadata_view` | `SELECT * FROM _pgmi_source_metadata` | Metadata inspection |

**Note:** `pgmi_plan_view` is moved from `schema.sql` to `api-v1.sql`. It references internal tables (`_pgmi_source`, `_pgmi_source_metadata`) but exposes a stable interface. The view contains execution ordering logic (UNNEST sort_keys, ROW_NUMBER, etc.).

### V1 API Functions

Functions that are part of the v1 contract. Only `pgmi_test_generate` lives in `api-v1.sql`; the others are defined in `schema.sql`:
- `pgmi_test_plan(p_pattern)`
- `pgmi_is_sql_file(filename)`
- `pgmi_persist_test_plan(target_schema, p_pattern)`
- `pgmi_test_callback(e)` - default callback
- `pgmi_test_generate(p_pattern, p_callback)` - generates macro replacement code

**Note:** Parameter functions (`pgmi_declare_param`, `pgmi_get_param`) were removed. Parameters are accessed via `current_setting('pgmi.key', true)`. Templates handle declaration, validation, and defaults.

Internal functions (not part of public API, stay in `schema.sql`):
- `pgmi_register_file(...)` - called by pgmi only
- `pgmi_validate_pattern(...)` - internal helper
- `pgmi_has_tests(...)` - internal helper

---

## Macro Code Generation Architecture

### Previous State (the binary generates SQL)

The preprocessor found `pgmi_test()` or `CALL pgmi_test()` calls and replaced them with a large DO block containing loops, savepoints, and EXECUTE statements, written in the binary's own code. This meant:
- The binary needed to know internal table names
- Different API versions would require different code in the binary
- SQL generation logic was split between the binary and SQL

### Target State (SQL generates SQL)

Move code generation to a SQL function that returns the replacement code as TEXT:

```
┌─────────────────┐     ┌────────────────────────────┐     ┌──────────────────┐
│ preprocessor    │ ──► │ SELECT pgmi_test_generate( │ ──► │ pgmi replaces    │
│ finds macro     │     │   'pattern', 'callback')   │     │ macro with       │
│ pgmi_test(...)  │     │ Returns: DO $$ ... END $$  │     │ returned SQL     │
└─────────────────┘     └────────────────────────────┘     └──────────────────┘
```

### The Generator Function (in `api-v1.sql`)

```sql
CREATE OR REPLACE FUNCTION pg_temp.pgmi_test_generate(
    p_pattern TEXT DEFAULT NULL,
    p_callback TEXT DEFAULT 'pg_temp.pgmi_test_callback'
) RETURNS TEXT
LANGUAGE plpgsql AS $$
DECLARE
    v_callback_safe TEXT;
    v_pattern_literal TEXT;
BEGIN
    -- Escape/validate inputs
    v_callback_safe := COALESCE(p_callback, 'pg_temp.pgmi_test_callback');
    v_pattern_literal := CASE
        WHEN p_pattern IS NULL THEN 'NULL'
        ELSE format('%L', p_pattern)
    END;

    -- Return the complete DO block as TEXT
    -- This references internal tables (_pgmi_test_source, etc.)
    RETURN format($code$
DO $pgmi_test$
DECLARE
    v_step RECORD;
    v_event pg_temp.pgmi_test_event;
    v_savepoint_name TEXT;
    v_ordinal INT := 0;
BEGIN
    -- Suite start
    v_event := ROW('suite_start', NULL, NULL, 0, 0, NULL)::pg_temp.pgmi_test_event;
    PERFORM %s(v_event);

    FOR v_step IN (
        SELECT * FROM pg_temp.pgmi_test_plan(%s)
    )
    LOOP
        v_ordinal := v_ordinal + 1;
        v_savepoint_name := 'pgmi_test_' || v_ordinal;

        CASE v_step.step_type
            WHEN 'fixture' THEN
                v_event := ROW('fixture_start', v_step.script_path, v_step.directory, v_step.depth, v_ordinal, NULL);
                PERFORM %s(v_event);
                EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = v_step.script_path);

            WHEN 'test' THEN
                SAVEPOINT _test_sp;
                v_event := ROW('test_start', v_step.script_path, v_step.directory, v_step.depth, v_ordinal, NULL);
                PERFORM %s(v_event);
                BEGIN
                    EXECUTE (SELECT content FROM pg_temp._pgmi_test_source WHERE path = v_step.script_path);
                    v_event := ROW('test_end', v_step.script_path, v_step.directory, v_step.depth, v_ordinal, NULL);
                    PERFORM %s(v_event);
                EXCEPTION WHEN OTHERS THEN
                    RAISE;
                END;
                ROLLBACK TO SAVEPOINT _test_sp;

            WHEN 'teardown' THEN
                v_event := ROW('teardown_start', NULL, v_step.directory, v_step.depth, v_ordinal, NULL);
                PERFORM %s(v_event);
        END CASE;
    END LOOP;

    -- Suite end
    v_event := ROW('suite_end', NULL, NULL, 0, v_ordinal, NULL)::pg_temp.pgmi_test_event;
    PERFORM %s(v_event);
END $pgmi_test$;
$code$,
        v_callback_safe,      -- suite_start callback
        v_pattern_literal,    -- pattern for pgmi_test_plan
        v_callback_safe,      -- fixture callback
        v_callback_safe,      -- test_start callback
        v_callback_safe,      -- test_end callback
        v_callback_safe,      -- teardown callback
        v_callback_safe       -- suite_end callback
    );
END;
$$;

COMMENT ON FUNCTION pg_temp.pgmi_test_generate IS
'Generates the SQL code for pgmi_test() macro expansion.
Called by the preprocessor, returns a DO block as TEXT.
Part of the versioned API contract - v1 generates code referencing v1 internal tables.';
```

> **Implementation Note:** The actual implementation in `api-v1.sql` differs from this proposal. Instead of generating a single DO block containing SAVEPOINT commands (which would fail because PL/pgSQL doesn't support savepoints), the actual implementation generates a series of top-level SQL statements where SAVEPOINT/ROLLBACK are at the SQL level and each fixture or test runs through a constant `DO` block that `EXECUTE`s its content (the path is passed through `set_config('pgmi_test.path', …)`, never inside the dollar quote). See `crates/pgmi-shared/contract/api-v1.sql` for the working implementation.

### Expansion flow

pgmi finds each `CALL pgmi_test(...)` in deploy.sql, calls
`SELECT pg_temp.pgmi_test_generate($1, $2)` with the pattern and the callback
as bind parameters, and puts the returned SQL in the macro's place.

### Benefits of This Approach

1. **Version isolation:** v1 generator knows v1 internals, v2 generator knows v2 internals
2. **The binary becomes version-agnostic:** Just calls `pgmi_test_generate()`, doesn't know table names
3. **SQL generation in SQL:** More natural, easier to maintain and test
4. **Testable:** Unit test the generator function independently
5. **Consistent behavior:** Generated code is deterministic and versioned

---

## Implementation

The binary embeds one contract file per version (`crates/pgmi-shared/contract/api-v1.sql`).
`--compat` names the version; an empty value means the latest, and a version
the binary does not ship is refused before connecting (exit 10,
`unsupported API version "99"; supported: 1`). After `schema.sql`, pgmi runs
the chosen contract file in the session.

### Execution Order

```
1. schema.sql          → Creates internal tables (_pgmi_*), internal functions
2. pgmi inserts files  → _pgmi_source via pgmi_register_file()
3. pgmi inserts params → _pgmi_parameter
4. api-v{N}.sql        → Creates versioned views, public functions, pgmi_test_generate()
5. preprocessor        → Calls pgmi_test_generate() to expand macros in deploy.sql
6. deploy.sql executes → Uses views (pgmi_source_view, pgmi_plan_view, etc.)
```

**Critical:** Step 4 (api-v{N}.sql) must execute BEFORE step 5 (preprocessing) because the preprocessor calls `pgmi_test_generate()` which is defined in the API contract.


## Template Updates

### Advanced Template (`deploy.sql`)

Change all references:
```sql
-- Before
FROM pg_temp.pgmi_source WHERE ...

-- After
FROM pg_temp.pgmi_source_view WHERE ...
```

Affected locations in advanced template:
- Parameter loading (the list `pg_temp.parameter_declaration()` in `deploy/parameters.sql`)
- Deploy function (joins `pgmi_plan_view` with `pgmi_source_view`)

### Basic Template (`deploy.sql`)

Similar changes - update `pgmi_source` to `pgmi_source_view`.

---

## Documentation Updates

### User-Facing Documentation

Only document the public API:
- Views: `pgmi_source_view`, `pgmi_parameter_view`, `pgmi_plan_view`, `pgmi_test_source_view`
- Functions: `pgmi_test_plan()`, `pgmi_test_generate()`
- Parameter access: `current_setting('pgmi.key', true)`

Do NOT document:
- Internal tables (`_pgmi_*`)
- Internal functions (`pgmi_register_file`, `pgmi_validate_pattern`, `pgmi_has_tests`)

### CLAUDE.md Updates

Update the "Session-Centric Deployment Model" section to reference views instead of tables.

### `pgmi ai` Output

Update embedded documentation to reference the public API only.

---

## Testing Strategy

The unit tests cover loading a valid version, the latest for an empty value,
and the refusal of an unsupported one. The integration tests deploy both
templates with and without `--compat=1` and check that each view returns what
its internal table holds.

---

## Implementation History

### Phase 1: SQL Refactoring

1. **Rename tables in `schema.sql`:**
   - `pgmi_source` → `_pgmi_source`
   - `pgmi_source_metadata` → `_pgmi_source_metadata`
   - `pgmi_parameter` → `_pgmi_parameter`
   - `pgmi_test_directory` → `_pgmi_test_directory`
   - `pgmi_test_source` → `_pgmi_test_source`
   - Update `pgmi_register_file()` to reference `_pgmi_source`
   - Update DROP TABLE statements at top

2. **Update internal function references in `schema.sql`:**
   - `pgmi_has_tests()` → reference `_pgmi_test_directory`, `_pgmi_test_source`
   - `pgmi_test_plan()` → reference `_pgmi_test_directory`, `_pgmi_test_source`
   - ~~`pgmi_declare_param()` → reference `_pgmi_parameter`~~ (Removed - templates handle parameters)

3. **Create `api-v1.sql`:**
   - Move `pgmi_plan_view` from `schema.sql` (update to reference `_pgmi_*`)
   - Add simple views: `pgmi_source_view`, `pgmi_parameter_view`, etc.
   - Move public functions: `pgmi_test_plan()`, etc. (Note: `pgmi_get_param()` and `pgmi_declare_param()` were removed)
   - Add `pgmi_test_generate()` function (migrate logic from the preprocessor)
   - Add `pgmi_test_callback()` default callback

4. **Update the file loader** to insert into `_pgmi_test_source` and `_pgmi_test_directory`.

### Phase 2: Loading the contract

5. Load `api-v{N}.sql` for the requested version after `schema.sql`.
6. Expand `CALL pgmi_test()` by calling `pgmi_test_generate()`, with no SQL
   generated in the binary.
7. Add the `--compat` flag, defaulting to the latest version.

### Phase 3: Templates and Testing

9. **Update templates:**
   - Advanced: `pgmi_source` → `pgmi_source_view`
   - Basic: `pgmi_source` → `pgmi_source_view`
   - Verify `CALL pgmi_test()` still works (now via generator)

10. **Run full test suite:**
    - `make test` - unit tests
    - `make test-integration` - template deployment
    - Fix any regressions

### Phase 4: Documentation

11. **Update documentation:**
    - CLAUDE.md - reference views, not tables
    - `crates/pgmi-shared/ai/` - update embedded docs
    - Template README files

12. **Add version-specific tests:**
    - Test `--compat=1` explicitly
    - Test unsupported version error
    - Test default version behavior

---

## Acceptance Criteria (Completed)

### CLI & Versioning
- [x] `--compat=1` flag accepted by `pgmi deploy`
- [x] Omitting `--compat` uses latest stable version
- [x] `--compat=99` returns clear error with supported versions list

### Schema Structure
- [x] Internal tables prefixed with `_` (`_pgmi_source`, `_pgmi_parameter`, etc.)
- [x] Public views created (`pgmi_source_view`, `pgmi_parameter_view`, etc.)
- [x] `pgmi_plan_view` in `api-v1.sql`, references internal tables
- [x] `pgmi_test_generate()` function exists and returns valid SQL

### Macro Expansion
- [x] `CALL pgmi_test()` expands via `pgmi_test_generate()` function call
- [x] The preprocessor no longer contains inline SQL generation
- [x] Generated code references internal tables (`_pgmi_test_source`)

### Templates
- [x] Advanced template uses `pgmi_source_view` (not `pgmi_source`)
- [x] Basic template uses `pgmi_source_view` (not `pgmi_source`)
- [x] `TestTemplateDeployment` passes for both templates

### Documentation
- [x] `pgmi ai` output only references public API (views, public functions)
- [x] CLAUDE.md references views, not tables
- [x] No references to `_pgmi_*` tables in user-facing documentation

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Breaking existing deploy.sql scripts | Templates updated in same release; clear migration guide |
| Performance overhead from views | Views are simple `SELECT *` - optimizer eliminates overhead |
| Complexity of maintaining multiple versions | Start with v1 only; add v2 when actually needed |
| Forgetting to update templates | Integration tests catch missing view references |
| `pgmi_test_generate()` SQL correctness | Port the existing preprocessor tests; verify generated SQL matches current output |
| Preprocessor now requires DB connection | Already true (files loaded before preprocessing) |

---

## Future Considerations

- **V2 API:** Only create when there's a breaking change that can't be backward-compatible
- **Version deprecation warnings:** `RAISE WARNING 'API v1 deprecated, migrate to v2'`
- **Version discovery:** `pgmi --supported-api-versions` command
- **Per-project version pinning:** `.pgmi.yaml` with `api-version: 1`
