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

## Two execution models

`deploy.sql` ships with the first one. Switching to the second is three lines.

| | **Re-run every deploy** (default) | **Apply once** (uncomment `(A)`, `(B)`, `(C)`) |
|---|---|---|
| What runs | Every migration, every time | Only migrations not yet recorded in `_migration` |
| Requires | Migrations to be **idempotent** (`CREATE TABLE IF NOT EXISTS`, `CREATE OR REPLACE FUNCTION`) | Nothing — a migration may be a one-shot `INSERT` |
| Ledger | None. Nothing to drift from reality | `_migration` (path, checksum, applied_at) |
| Editing an applied migration | Takes effect on the next deploy | **Does nothing** — the file is skipped. The stored checksum shows it changed |
| Feels like | `psql -f` over a directory | Flyway / Sqitch |

**Which do you want?**

Start with the default. It is simpler, and "every file is idempotent" is a
property you can actually check by reading the file — unlike "this ledger
matches what is really in the database", which you cannot.

Switch to apply-once when a migration genuinely cannot be re-run: a data
backfill, a one-shot `INSERT`, an `ALTER TABLE` that is not conditional. That is
the moment tracking earns its keep.

You are not choosing a framework either way. The tracking block is three lines of
SQL in *your* `deploy.sql` — read it, change it, delete it. If you want a
checksum mismatch to fail the deploy instead of being ignored, that is your `IF`
statement to write.

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
