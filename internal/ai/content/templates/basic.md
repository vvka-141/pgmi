# Basic Template - AI Guide

> Simple pgmi project structure for learning and prototyping.

## When to Use

- Learning pgmi for the first time
- Simple migration-only projects
- Quick prototypes
- Teams new to PostgreSQL-native development

## Project Structure

```
myproject/
├── deploy.sql                  # Orchestrates deployment
├── pgmi.yaml                   # Connection and parameter defaults
├── migrations/
│   ├── 001_users.sql           # Users table
│   └── 002_user_crud.sql       # CRUD functions
└── __test__/
    ├── _setup.sql              # Test fixtures (rolled back)
    └── test_user_crud.sql      # CRUD function tests
```

## How It Works

### 1. deploy.sql Orchestration

The `deploy.sql` file queries `pgmi_plan_view` and executes migrations:

```sql
DO $$
DECLARE
    v_file RECORD;
BEGIN
    FOR v_file IN (
        SELECT path, content
        FROM pg_temp.pgmi_plan_view
        WHERE path LIKE './migrations/%'
        ORDER BY execution_order
    )
    LOOP
        RAISE NOTICE 'Executing: %', v_file.path;
        EXECUTE v_file.content;
    END LOOP;
END $$;
```

### 2. Migrations

Migrations use standard PostgreSQL DDL with idempotent patterns:

```sql
-- 001_users.sql
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT UNIQUE NOT NULL,
    name TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);
```

### 3. Tests

Tests run in savepoints and roll back automatically:

```sql
-- __test__/test_user_crud.sql
DO $$
DECLARE
    v_user users;
BEGIN
    -- Insert test user
    SELECT * INTO v_user FROM upsert_user('test@example.com', 'Test');

    IF v_user.email != 'test@example.com' THEN
        RAISE EXCEPTION 'TEST FAILED: User not created';
    END IF;

    RAISE NOTICE 'User CRUD tests passed';
END $$;
```

## Key Conventions

### File Naming

| Pattern | Purpose |
|---------|---------|
| `migrations/NNN_*.sql` | Ordered migrations (lexicographic) |
| `__test__/_setup.sql` | Test fixtures (REQUIRED name) |
| `__test__/test_*.sql` | Test files |

### Idempotent Patterns

```sql
-- Tables
CREATE TABLE IF NOT EXISTS ...

-- Functions
CREATE OR REPLACE FUNCTION ...

-- Indexes
CREATE INDEX IF NOT EXISTS ...
```

### Parameter Usage

```sql
-- Access CLI parameters (use COALESCE for defaults)
v_email := COALESCE(current_setting('pgmi.admin_email', true), 'admin@example.com');

-- Use in migrations
INSERT INTO users (email, name)
VALUES (COALESCE(current_setting('pgmi.admin_email', true), 'admin@example.com'), 'Admin')
ON CONFLICT (email) DO NOTHING;
```

## Deployment Commands

```bash
# Initialize project
pgmi init myproject --template basic

# Deploy with default parameters
pgmi deploy ./myproject -c "postgresql://user:pass@host/db"

# Deploy with custom admin email
pgmi deploy ./myproject -c "..." --param admin_email=ops@company.com

# Run tests only
pgmi deploy ./myproject -c "..." --param run_tests=true
```

## Extending the Basic Template

### Add a New Migration

1. Create `migrations/003_posts.sql`:
```sql
CREATE TABLE IF NOT EXISTS posts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES users(id),
    title TEXT NOT NULL,
    body TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);
```

2. Redeploy: `pgmi deploy ./myproject -c "..."`

### Add Tests

1. Create `__test__/test_posts.sql`:
```sql
DO $$
BEGIN
    -- Test post creation
    INSERT INTO posts (user_id, title, body)
    SELECT id, 'Test Post', 'Test body'
    FROM users LIMIT 1;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'TEST FAILED: No users for post test';
    END IF;

    RAISE NOTICE 'Post tests passed';
END $$;
```

## Graduating to Advanced Template

When you need:
- REST/RPC/MCP API handlers
- Multi-schema architecture
- Row-level security
- Membership/authentication patterns

Consider migrating to the advanced template:
```bash
pgmi init newproject --template advanced
```

## See Also

- `pgmi ai skill pgmi-sql` - SQL conventions
- `pgmi ai template advanced` - Production patterns
- `pgmi templates describe basic` - Template details
