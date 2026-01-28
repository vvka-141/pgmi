# {{PROJECT_NAME}}

A minimal PostgreSQL deployment project powered by [pgmi](https://github.com/vvka-141/pgmi).

## Quick Start

Deploy with default greeting:
```bash
pgmi deploy
```

Deploy with custom greeting:
```bash
pgmi deploy --param name=Alice
```

You should see:
```
✓ Executing: ./migrations/001_hello_world.sql
✓ Migrations complete
✓ Testing: ./__test__/test_hello_world.sql
✓ hello_world() returns correct greeting: "Hello, Alice!"
✓ All tests passed
✓ Tests rolled back (clean database)
✓ Deployment complete!
```

## Try It Out

Connect to your database and test the function:
```sql
SELECT public.hello_world();
```

Output:
```
 hello_world
--------------
 Hello, Alice!
```

## What Just Happened?

When you ran `pgmi deploy`, here's what occurred:

1. **pgmi loaded** all project files into session-scoped temp tables (`pgmi_source`, `pgmi_unittest_script`)
2. **Template expansion** replaced `$` `{name}` with your parameter value (default: 'World')
3. **Session variables** were set - parameters are now accessible via `current_setting('pgmi.name')`
4. **Single transaction** deployed migrations and ran tests atomically
5. **Savepoint + rollback** validated migrations work without persisting test artifacts
6. **Commit** saved migrations, discarded test data (clean database)

This demonstrates pgmi's core capabilities:
- **Dual parameter access** - both template expansion AND runtime session variables
- **Session-based execution** using PostgreSQL temporary tables
- **SQL-driven orchestration** where YOU control the deployment logic
- **Professional testing pattern** using savepoints (exactly how pgTAP works)
- **Transactional safety** - migrations succeed together or fail together

## Parameter Access: Template vs Runtime

pgmi parameters serve TWO purposes:

### 1. Template Expansion (Compile-Time)
Parameters replace `$` `{placeholders}` in your SQL files before execution:
```sql
-- Before: SELECT 'Hello, $' '{name}!'
-- After:  SELECT 'Hello, Alice!'
```

### 2. Session Variables (Runtime)
Parameters are also available as PostgreSQL session variables:
```sql
-- Access parameter value during deployment
SELECT current_setting('pgmi.name');  -- Returns: 'Alice'

-- Or use the convenience wrapper
SELECT pgmi_get_param('name', 'World');  -- Returns: 'Alice' or 'World' if not set

-- Use in conditional logic
DO $$
BEGIN
    IF current_setting('pgmi.env', true) = 'production' THEN
        RAISE NOTICE 'Production deployment detected';
    END IF;
END $$;
```

**Why both?** Template expansion generates code dynamically, while session variables enable runtime decisions based on parameters.

**Security Note:** Parameters are set as session variables (`pgmi.*`) and are visible via `SHOW ALL` and `pg_settings`. Do not pass sensitive values (passwords, API keys) as parameters. Use PostgreSQL connection strings or environment variables for secrets instead.

## What's in This Project?

```
{{PROJECT_NAME}}/
├── deploy.sql                      # Controls execution order and logic
├── migrations/
│   └── 001_hello_world.sql        # Creates hello_world() function
├── __test__/
│   └── test_hello_world.sql       # Validates the function works
└── README.md                       # This file
```

### deploy.sql
The orchestrator. This file decides:
- Which files to execute and in what order
- How to initialize parameters and resolve templates
- What phases to run (migrations, tests, etc.)
- Transaction boundaries

**You have complete control.** Change the execution order, add conditional logic, or skip tests—it's all up to you.

### migrations/
Put your schema definitions, functions, and data migrations here. Files execute in alphabetical order (controlled by deploy.sql).

### __test__/
Validation tests that run after migrations. These prove your deployment worked correctly. The `__test__/` directory name signals these are special framework-managed transactional tests.

## Next Steps

### Customize the Function
Edit `migrations/001_hello_world.sql` to create your own business logic:
```sql
CREATE OR REPLACE FUNCTION public.calculate_revenue(region TEXT)
RETURNS NUMERIC AS $$
    -- Your logic here
$$ LANGUAGE SQL;
```

### Add More Migrations
Create additional files—they execute alphabetically:
```bash
echo "CREATE TABLE users (id SERIAL PRIMARY KEY);" > migrations/002_create_tables.sql
echo "CREATE INDEX idx_users_id ON users(id);" > migrations/003_create_indexes.sql
```

### Customize Execution Logic
Edit `deploy.sql` to change how deployments work:
- Add conditional execution based on parameters
- Create multiple phases (setup → migrate → seed → test)
- Add error handling and rollback logic
- Execute files in custom order

### Add More Tests
Create tests in `__test__/` to validate your deployment - see the existing test file for examples.

### Explore Advanced Features
Ready for production-grade structure? Check out the **advanced** template:
```bash
pgmi init myapp --template advanced
```

The advanced template includes:
- Metadata-driven deployment with XML annotations
- Comprehensive test organization
- Idempotent patterns for roles and schemas
- Production-ready patterns

## Learn More

- [pgmi Documentation](https://github.com/vvka-141/pgmi)
- [Session API Reference](https://github.com/vvka-141/pgmi/blob/main/docs/session-api.md)
- [Metadata Guide](https://github.com/vvka-141/pgmi/blob/main/docs/METADATA.md)
