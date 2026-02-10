# {{PROJECT_NAME}}

A PostgreSQL project with users table, CRUD functions, and transactional tests.

## Quick Start

```bash
pgmi deploy --param admin_email=admin@yourcompany.com
```

## What This Does

1. Creates a `users` table
2. Creates `upsert_user()`, `get_user()`, `delete_user()` functions
3. Inserts an admin user using the `admin_email` parameter
4. Runs tests against fixture data, then rolls back test artifacts

## Project Structure

```
{{PROJECT_NAME}}/
├── deploy.sql                  # Orchestrates deployment
├── pgmi.yaml                   # Connection and parameter defaults
├── migrations/
│   ├── 001_users.sql           # Users table
│   └── 002_user_crud.sql       # CRUD functions
└── __test__/
    ├── _setup.sql              # Test data (rolled back)
    └── test_user_crud.sql      # CRUD function tests
```

## Try It

```sql
-- Create or update a user (idempotent)
SELECT upsert_user('jane@example.com', 'Jane');

-- Retrieve user
SELECT * FROM get_user('jane@example.com');

-- Delete user
SELECT delete_user('jane@example.com');
```

## Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| admin_email | admin@example.com | Email for initial admin user |

Override via CLI: `--param admin_email=ops@mycompany.com`

## Learn More

- [pgmi Documentation](https://github.com/vvka-141/pgmi)
- Advanced template: `pgmi init myapp --template advanced`
