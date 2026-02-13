# Change Request: API Versioning for pgmi Session Interface

> **Status: IMPLEMENTED**
> This design document describes changes that have been completed. The "Current State" section reflects the pre-implementation state for historical context. The "Target State" is now the actual implementation. See [session-api.md](../session-api.md) for current API documentation.

## Overview

Introduce versioned API contracts for pgmi's session-scoped interface (temp tables, views, functions). This enables:
- Stable deploy.sql scripts that don't break on pgmi upgrades
- DevOps pipelines pinned to specific API versions
- Internal refactoring freedom for pgmi maintainers

## Motivation

Currently, deploy.sql scripts directly reference internal tables like `pg_temp.pgmi_source`. If pgmi changes the table structure, all user deploy.sql scripts break. By introducing versioned views as the public API, pgmi can evolve internally while maintaining backward compatibility.

**Use case:** A CI/CD pipeline uses `pgmi deploy --compat=1`. Even when pgmi 2.0 ships with breaking internal changes, the pipeline continues working because pgmi provides the v1 API contract.

---

## Design Decisions (Already Made)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| CLI flag name | `--compat` | Industry standard (Kubernetes, REST APIs) |
| View naming | `pgmi_*_view` suffix | Explicit, consistent with existing `pgmi_plan_view` |
| Internal table naming | `_pgmi_*` underscore prefix | Convention: underscore = internal |
| Default version | Latest stable | New users get best experience |
| Deprecation policy | 100% backward compatible | No version removal, old versions always work |
| File naming | `api-v1.sql`, `api-v2.sql` | Major versions only, no semver in filenames |
| `pgmi_plan_view` location | Moves to `api-v1.sql` | It's part of the public API, references internal tables |
| Macro code generation | SQL function returns replacement code | Go calls `pgmi_test_generate()`, replaces macro with result |

---

## Pre-Implementation State (Historical)

### Internal Tables (in `internal/params/schema.sql`)

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
| `pgmi_register_file(...)` | Internal: Go calls to insert files |
| `pgmi_validate_pattern(pattern)` | Internal: Regex validation |
| `pgmi_has_tests(dir, pattern)` | Internal: Recursive test discovery |
| `pgmi_persist_test_plan(schema, pattern)` | Export test plan to permanent table |

**Note:** Parameter access is via `current_setting('pgmi.key', true)`. Templates handle their own declaration, validation, and defaults.

---

## Implemented State

### File Structure

```
internal/params/
  schema.sql          # Internal tables (_pgmi_*), always executes first
  api-v1.sql          # V1 views and public functions
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

### V1 API Functions (in `api-v1.sql`)

Functions that are part of the v1 contract:
- `pgmi_test_plan(pattern)`
- `pgmi_is_sql_file(filename)`
- `pgmi_persist_test_plan(schema, pattern)`
- `pgmi_test_callback(event)` - default callback
- `pgmi_test_generate(pattern, callback)` - generates macro replacement code

**Note:** Parameter functions (`pgmi_declare_param`, `pgmi_get_param`) were removed. Parameters are accessed via `current_setting('pgmi.key', true)`. Templates handle declaration, validation, and defaults.

Internal functions (not part of public API, stay in `schema.sql`):
- `pgmi_register_file(...)` - called by Go only
- `pgmi_validate_pattern(...)` - internal helper
- `pgmi_has_tests(...)` - internal helper

---

## Macro Code Generation Architecture

### Current State (Go generates SQL)

The Go preprocessor in `internal/preprocessor/` finds `pgmi_test()` or `CALL pgmi_test()` calls and replaces them with a large DO block containing loops, savepoints, and EXECUTE statements. This means:
- Go needs to know internal table names
- Different API versions would require different Go code
- SQL generation logic is split between Go and SQL

### Target State (SQL generates SQL)

Move code generation to a SQL function that returns the replacement code as TEXT:

```
┌─────────────────┐     ┌────────────────────────────┐     ┌──────────────────┐
│ Go preprocessor │ ──► │ SELECT pgmi_test_generate( │ ──► │ Go replaces      │
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
Called by Go preprocessor, returns a DO block as TEXT.
Part of the versioned API contract - v1 generates code referencing v1 internal tables.';
```

> **Implementation Note:** The actual implementation in `api-v1.sql` differs from this proposal. Instead of generating a single DO block containing SAVEPOINT commands (which would fail because PL/pgSQL doesn't support savepoints), the actual implementation generates a series of top-level SQL statements where SAVEPOINT/ROLLBACK are at the SQL level and test content is wrapped in separate DO blocks using EXECUTE. See `internal/contract/api-v1.sql` for the working implementation.

### Updated Go Preprocessor Flow

```go
// internal/preprocessor/preprocessor.go

func (p *Preprocessor) ExpandMacros(ctx context.Context, conn *pgxpool.Conn, sql string) (string, error) {
    // Find all pgmi_test(...) or CALL pgmi_test(...) calls
    matches := pgmiTestRegex.FindAllStringSubmatchIndex(sql, -1)

    for _, match := range matches {
        pattern, callback := extractArguments(sql, match)

        // Call the SQL generator function (part of versioned API)
        var generatedSQL string
        err := conn.QueryRow(ctx,
            "SELECT pg_temp.pgmi_test_generate($1, $2)",
            pattern, callback,
        ).Scan(&generatedSQL)
        if err != nil {
            return "", fmt.Errorf("macro expansion failed: %w", err)
        }

        // Replace macro with generated SQL
        sql = replaceMacro(sql, match, generatedSQL)
    }

    return sql, nil
}
```

### Benefits of This Approach

1. **Version isolation:** v1 generator knows v1 internals, v2 generator knows v2 internals
2. **Go becomes version-agnostic:** Just calls `pgmi_test_generate()`, doesn't know table names
3. **SQL generation in SQL:** More natural, easier to maintain and test
4. **Testable:** Unit test the generator function independently
5. **Consistent behavior:** Generated code is deterministic and versioned

---

## Go Implementation

### New Package: `internal/contract`

```go
// internal/contract/contract.go
package contract

import "embed"

//go:embed api-v1.sql
//go:embed api-v2.sql
var embeddedAPIs embed.FS

type Version string

const (
    V1      Version = "1"
    Latest  Version = V1  // Update when new version released
)

var supportedVersions = map[Version]string{
    V1: "api-v1.sql",
}

type Contract struct {
    Version Version
    SQL     string
}

// Load returns the API contract for the specified version.
// Returns error if version is not supported.
func Load(version string) (*Contract, error) {
    v := Version(version)
    if v == "" {
        v = Latest
    }

    filename, ok := supportedVersions[v]
    if !ok {
        return nil, fmt.Errorf("unsupported API version %q; supported: %v",
            version, SupportedVersions())
    }

    content, err := embeddedAPIs.ReadFile(filename)
    if err != nil {
        return nil, fmt.Errorf("failed to load API v%s: %w", version, err)
    }

    return &Contract{Version: v, SQL: string(content)}, nil
}

// SupportedVersions returns list of supported API versions.
func SupportedVersions() []Version {
    versions := make([]Version, 0, len(supportedVersions))
    for v := range supportedVersions {
        versions = append(versions, v)
    }
    sort.Slice(versions, func(i, j int) bool {
        return versions[i] < versions[j]
    })
    return versions
}
```

### CLI Flag Addition

In `internal/cli/deploy.go`:

```go
var compat string

func init() {
    deployCmd.Flags().StringVar(&compat, "compat", "",
        "Compatibility level (default: latest)")
}
```

### Deployer Integration

In `internal/services/deployer.go`, after executing `schema.sql`:

```go
// Load and execute API contract
contract, err := contract.Load(cfg.Compat)
if err != nil {
    return fmt.Errorf("API contract error: %w", err)
}

if _, err := conn.Exec(ctx, contract.SQL); err != nil {
    return fmt.Errorf("failed to initialize API v%s: %w", contract.Version, err)
}
```

### Execution Order

```
1. schema.sql          → Creates internal tables (_pgmi_*), internal functions
2. Go inserts files    → _pgmi_source via pgmi_register_file()
3. Go inserts params   → _pgmi_parameter
4. api-v{N}.sql        → Creates versioned views, public functions, pgmi_test_generate()
5. Go preprocessor     → Calls pgmi_test_generate() to expand macros in deploy.sql
6. deploy.sql executes → Uses views (pgmi_source_view, pgmi_plan_view, etc.)
```

**Critical:** Step 4 (api-v{N}.sql) must execute BEFORE step 5 (preprocessing) because the preprocessor calls `pgmi_test_generate()` which is defined in the API contract.

### Loader Code Updates

`internal/files/loader/loader.go` directly references table names for batch inserts:

| Current | New |
|---------|-----|
| `pg_temp.pgmi_test_source` | `pg_temp._pgmi_test_source` |
| `pg_temp.pgmi_test_directory` | `pg_temp._pgmi_test_directory` |

The `pgmi_register_file()` function (stays in `schema.sql`) handles `_pgmi_source` internally, so loader code calling it doesn't need changes for that table.

---

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
- Parameter loading (reads `pgmi_source_view` for `session.xml`)
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

### Unit Tests (`internal/contract/contract_test.go`)

```go
func TestLoad_ValidVersion(t *testing.T) {
    c, err := contract.Load("1")
    require.NoError(t, err)
    assert.Equal(t, contract.V1, c.Version)
    assert.Contains(t, c.SQL, "pgmi_source_view")
}

func TestLoad_EmptyVersionUsesLatest(t *testing.T) {
    c, err := contract.Load("")
    require.NoError(t, err)
    assert.Equal(t, contract.Latest, c.Version)
}

func TestLoad_UnsupportedVersion(t *testing.T) {
    _, err := contract.Load("99")
    require.Error(t, err)
    assert.Contains(t, err.Error(), "unsupported API version")
}

func TestSupportedVersions(t *testing.T) {
    versions := contract.SupportedVersions()
    assert.Contains(t, versions, contract.V1)
}
```

### Integration Tests

1. **Template deployment with default version:**
   - Run existing `TestTemplateDeployment` - should pass unchanged

2. **Template deployment with explicit version:**
   ```go
   func TestTemplateDeployment_ExplicitCompat(t *testing.T) {
       // Deploy with --compat=1
       // Verify views exist
       // Verify deploy.sql can query pgmi_source_view
   }
   ```

3. **Unsupported version error:**
   ```go
   func TestDeploy_UnsupportedCompat(t *testing.T) {
       // Deploy with --compat=99
       // Expect clear error message listing supported versions
   }
   ```

4. **View functionality:**
   ```go
   func TestAPIViews_MatchTableContent(t *testing.T) {
       // Insert test data into _pgmi_source
       // Query pgmi_source_view
       // Verify identical results
   }
   ```

### Regression Tests

Run full test suite to ensure nothing breaks:
```bash
make test-all
make test-integration
```

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
   - Add `pgmi_test_generate()` function (migrate logic from Go preprocessor)
   - Add `pgmi_test_callback()` default callback

4. **Update loader code (`internal/files/loader/loader.go`):**
   - `pgmi_test_source` → `_pgmi_test_source`
   - `pgmi_test_directory` → `_pgmi_test_directory`

### Phase 2: Go Infrastructure

5. **Create `internal/contract` package:**
   - Embed `api-v1.sql`
   - `Load(version)` function
   - `SupportedVersions()` function
   - Unit tests

6. **Update deployer (`internal/services/deployer.go`):**
   - Execute `schema.sql` first
   - Execute `api-v{N}.sql` second
   - Pass API version from config

7. **Update preprocessor (`internal/preprocessor/`):**
   - Remove inline SQL generation
   - Call `pgmi_test_generate()` to get replacement code
   - Simplify to: find macro → call function → replace

8. **Add `--compat` CLI flag:**
   - Add to `internal/cli/deploy.go`
   - Wire through config to deployer
   - Default to latest

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
    - `internal/ai/content/` - update embedded docs
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
- [x] Go preprocessor no longer contains inline SQL generation
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
| `pgmi_test_generate()` SQL correctness | Port existing Go tests; verify generated SQL matches current output |
| Preprocessor now requires DB connection | Already true (files loaded before preprocessing) |

---

## Future Considerations

- **V2 API:** Only create when there's a breaking change that can't be backward-compatible
- **Version deprecation warnings:** `RAISE WARNING 'API v1 deprecated, migrate to v2'`
- **Version discovery:** `pgmi --supported-api-versions` command
- **Per-project version pinning:** `.pgmi.yaml` with `api-version: 1`

---

## Estimated Effort

| Phase | Effort |
|-------|--------|
| Phase 1: SQL Refactoring | 3-4 hours |
| Phase 2: Go Infrastructure | 3-4 hours |
| Phase 3: Templates and Testing | 2-3 hours |
| Phase 4: Documentation | 1-2 hours |
| **Total** | **9-13 hours** |

**Note:** The `pgmi_test_generate()` function is the most complex piece - it must produce identical output to the current Go preprocessor. Recommend implementing incrementally with comparison tests.

---

## References

### Files to Modify

| File | Changes |
|------|---------|
| `internal/params/schema.sql` | Rename tables to `_pgmi_*`, update internal function references |
| `internal/params/api-v1.sql` | **NEW:** Views, public functions, `pgmi_test_generate()` |
| `internal/files/loader/loader.go` | Update table references to `_pgmi_*` |
| `internal/preprocessor/preprocessor.go` | Replace inline SQL with `pgmi_test_generate()` call |
| `internal/services/deployer.go` | Execute `api-v{N}.sql` after schema, before preprocessing |
| `internal/cli/deploy.go` | Add `--compat` flag |
| `internal/contract/contract.go` | **NEW:** API version management |
| `internal/scaffold/templates/advanced/deploy.sql` | `pgmi_source` → `pgmi_source_view` |
| `internal/scaffold/templates/basic/deploy.sql` | `pgmi_source` → `pgmi_source_view` |
| `CLAUDE.md` | Update session model documentation |
| `internal/ai/content/` | Update embedded AI documentation |

### Key Test Files

| File | Purpose |
|------|---------|
| `internal/contract/contract_test.go` | **NEW:** Version loading tests |
| `internal/preprocessor/preprocessor_test.go` | Update for new expansion approach |
| `internal/scaffold/integration_test.go` | Template deployment verification |
| `internal/params/schema_test.go` | Schema object tests |

### Grep Commands for Discovery

```bash
# Find all references to pgmi_source (not _pgmi_source)
grep -r "pgmi_source" --include="*.go" --include="*.sql" | grep -v "_pgmi_source"

# Find all references to pgmi_parameter
grep -r "pgmi_parameter" --include="*.go" --include="*.sql" | grep -v "_pgmi_parameter"

# Find preprocessor SQL generation
grep -r "pgmi_test" internal/preprocessor/
```
