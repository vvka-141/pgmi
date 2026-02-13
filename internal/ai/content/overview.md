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
pgmi deploy ./myproject -c "postgresql://user:pass@host/db"

# Run tests
pgmi deploy ./myproject -c "..." --param run_tests=true
```

## Core Concepts

### Session-Scoped Tables

pgmi creates temporary tables in `pg_temp` schema:

| Table | Purpose |
|-------|---------|
| `pgmi_source` | All SQL files with path, content, metadata |
| `pgmi_plan_view` | VIEW ordering files for execution |
| `pgmi_parameter` | CLI parameters (`--param key=value`) |
| `pgmi_test_source` | Test files from `__test__/` directories |

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
v_env := pg_temp.pgmi_get_param('env', 'development');

-- Conditional logic based on parameters
IF pg_temp.pgmi_get_param('run_tests', 'false') = 'true' THEN
    pgmi_test();
END IF;
```

## Available Skills

Use `pgmi ai skill <name>` to get detailed guidance:

| Skill | Use When |
|-------|----------|
| `pgmi-sql` | Writing SQL/PL/pgSQL code |
| `pgmi-philosophy` | Understanding architectural decisions |
| `pgmi-cli` | Adding CLI commands or flags |
| `pgmi-templates` | Creating/modifying scaffold templates |
| `pgmi-testing-review` | Writing or reviewing tests |
| `pgmi-postgres-review` | Reviewing SQL for correctness |
| `pgmi-api-architecture` | REST/RPC/MCP protocol design |

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
pgmi init <name> [--template basic|advanced]  # Create project
pgmi deploy <path> -c <conn> [-d <db>]        # Deploy to database
pgmi templates list                            # List templates
pgmi templates describe <name>                 # Template details
pgmi ai skills                                 # List AI skills
pgmi ai skill <name>                           # Get skill content
```

## Learn More

- `pgmi ai skill pgmi-sql` - Complete SQL conventions
- `pgmi ai skill pgmi-philosophy` - Architectural principles
- `pgmi ai template advanced` - Production template guide
