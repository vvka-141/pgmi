# pgmi - AI Assistant Guide

> PostgreSQL-native execution fabric for database deployments. Minimal interference, maximum empowerment.

## What is pgmi?

pgmi loads SQL files and parameters into PostgreSQL session-scoped temporary tables, then executes a user-provided `deploy.sql` that orchestrates deployment using PostgreSQL's procedural languages.

**Key insight:** pgmi is an execution fabric, NOT a migration framework. Transaction control, execution order, retry logic, idempotency - all belong in your SQL, not CLI flags.

## Quick Start for AI Assistants

```bash
# Initialize a project
pgmi init myproject --template basic

# Deploy to database
pgmi deploy ./myproject --connection "postgresql://user:pass@host/db"

# Run tests
pgmi deploy ./myproject --connection "..." --param run_tests=true
```

## Core Concepts

### Session-Scoped Tables

pgmi creates temporary tables in `pg_temp` schema:

| View | Purpose |
|------|---------|
| `pgmi_source_view` | All SQL files with path, content, metadata (excludes deploy.sql and `__test__/`) |
| `pgmi_plan_view` | Execution order derived from `<pgmi-meta>` sortKeys |
| `pgmi_parameter_view` | CLI parameters (`--param key=value`) |
| `pgmi_test_source_view` | Test files from `__test__/` directories |
| `pgmi_test_directory_view` | Test directory hierarchy |
| `pgmi_source_metadata_view` | Parsed `<pgmi-meta>` blocks |

All names end in `_view` — they are the stable public API. Do not query the
`_pgmi_*` internal tables directly; they are implementation details.

### deploy.sql Pattern

```sql
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    ) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### Parameters

```sql
-- Access parameters with defaults
v_env := COALESCE(current_setting('pgmi.env', true), 'development');

-- Conditional logic based on parameters
IF COALESCE(current_setting('pgmi.run_tests', true), 'false') = 'true' THEN
    CALL pgmi_test();
END IF;
```

## Available Skills

Use `pgmi ai skill <name>` to get detailed guidance:

| Skill | Use When |
|-------|----------|
| `pgmi-sql` | Writing SQL/PL/pgSQL or deploy.sql |
| `pgmi-philosophy` | Understanding architectural decisions |
| `pgmi-system-design` | Designing features the pgmi way (physical/logical/API layering) |
| `pgmi-templates` | Creating or modifying scaffold templates |
| `pgmi-testing-review` | Writing, organizing, or debugging tests |
| `pgmi-postgres-review` | Writing SQL with correctness and performance guidance |
| `pgmi-metadata-system` | Working with `<pgmi-meta>` blocks, sortKeys, execution ordering |
| `pgmi-test-architecture` | Organizing `__test__/` directories and test strategy |
| `postgresql-patterns` | EXECUTE, format(), composite types, dynamic SQL |
| `pgmi-api-architecture` | REST/RPC/MCP protocol design (advanced template) |
| `pgmi-mcp` | MCP handler implementation (advanced template) |

## SQL Conventions

### Table Names: Singular

```sql
-- CORRECT: Singular table names
CREATE TABLE account (...);
CREATE TABLE "user" (...);  -- Quote reserved words

-- WRONG: Plural names
CREATE TABLE accounts (...);
```

### Test Fixtures: `_setup.sql`

```
__test__/
  _setup.sql           # REQUIRED name for fixtures
  test_something.sql   # Test files
```

### Dollar-Quoting

```sql
-- Always use dollar-quoting for embedded SQL
DO $outer$
BEGIN
    EXECUTE $sql$SELECT * FROM users$sql$;
END $outer$;
```

### JSON Keys: camelCase

```sql
-- PostgreSQL identifiers: snake_case
-- JSON keys: camelCase
jsonb_build_object(
    'httpMethod', '^GET$',
    'autoLog', true
)
```

## Commands Reference

```bash
pgmi init <name> [--template basic|advanced]   # Create project
pgmi deploy <path> --connection <conn>         # Deploy to database
pgmi templates list                            # List templates
pgmi templates describe <name>                 # Template details
pgmi metadata scaffold <path> [--write]        # Generate/inject <pgmi-meta> blocks
pgmi metadata validate <path> [--json]         # Validate metadata (no DB needed)
pgmi metadata plan <path> [--json]             # Show execution order (no DB needed)
pgmi ai skills                                 # List AI skills
pgmi ai skill <name>                           # Get skill content
pgmi ai client [lang]                          # API client guidance (ts, python, go, csharp, rust)
pgmi ai setup                                  # Write guidance into .claude/skills/pgmi/
pgmi ai setup --assistant agents               # Write AGENTS.md (Codex, opencode, etc.)
pgmi ai setup --all                            # Write guidance for every assistant
pgmi ai check                                  # Report whether that guidance is current
```

## Learn More

- `pgmi ai skill pgmi-sql` - Complete SQL conventions
- `pgmi ai skill pgmi-philosophy` - Architectural principles
- `pgmi ai skill pgmi-templates` - Production template guide
- `pgmi ai client [lang]` - Consuming the API from code? Client guidance for TypeScript, Python, Go, C#, Rust
