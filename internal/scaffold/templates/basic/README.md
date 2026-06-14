# {{PROJECT_NAME}}

A PostgreSQL project deployed by pgmi — with environment-aware logic, project data loading, and transactional tests.

## Quick Start

```bash
pgmi deploy --param admin_email=you@example.com
```

Production (skips dev seeding):
```bash
pgmi deploy --param env=production
```

## What This Does

1. Reads `project.json` through `pgmi_source_view` (pgmi loads all project files, not just SQL)
2. Runs migrations in `migrations/` in path order
3. Seeds an admin user in non-production environments
4. Runs tests against fixture data, then rolls back test artifacts

## Project Structure

```
{{PROJECT_NAME}}/
├── deploy.sql                  # Orchestrates deployment (env branching, data loading)
├── project.json                # Project metadata (read by deploy.sql, not by pgmi)
├── pgmi.yaml                   # Connection and parameter defaults
├── migrations/
│   ├── 001_users.sql           # Users table
│   └── 002_user_crud.sql       # CRUD functions
└── __test__/
    ├── _setup.sql              # Test data (rolled back)
    └── test_user_crud.sql      # CRUD + project data tests
```

## Parameters

| Parameter | Default | Description |
|-----------|---------|-------------|
| env | development | Environment name; `production` skips dev seeding |
| admin_email | admin@example.com | Email for initial admin user (non-production only) |

Override via CLI: `--param env=production --param admin_email=ops@mycompany.com`

## Learn More

- [pgmi Documentation](https://github.com/vvka-141/pgmi)
- Advanced template: `pgmi init myapp --template advanced`
