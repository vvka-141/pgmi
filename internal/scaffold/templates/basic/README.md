# {{PROJECT_NAME}}

A PostgreSQL project deployed by pgmi — with environment-aware logic, project data loading, and transactional tests.

## Quick Start

Set your database password and deploy:

```bash
export PGPASSWORD="your-postgres-password"    # or use a connection string
pgmi deploy . -d {{PROJECT_NAME}} --overwrite --force
```

`--overwrite --force` drops and recreates the database — use only for local development.
For an existing database, deploy incrementally (no `--overwrite`):

```bash
pgmi deploy . -d {{PROJECT_NAME}} --param env=production
```

Edit `pgmi.yaml` to set connection defaults (host, port, username) so you
don't need to repeat them on every command.

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

## Working with an AI assistant

```bash
pgmi ai setup          # writes .claude/skills/pgmi/ — commit it
```

The skill teaches an assistant pgmi's conventions (deploy.sql owns the
transaction, tests run in the same session, `pgmi_source_view` and friends)
before it edits your SQL. `--assistant cursor|copilot|windsurf|cline` targets
other tools; `pgmi ai check` reports whether it is current.

To read the same docs yourself, run `pgmi ai`.

## Learn More

- [pgmi Documentation](https://github.com/vvka-141/pgmi)
- Advanced template: `pgmi init myapp --template advanced`
