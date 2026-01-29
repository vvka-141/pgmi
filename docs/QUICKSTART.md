# pgmi Quick Start

## Prerequisites

- Go 1.21+ installed
- PostgreSQL database accessible
- Connection credentials

## Setup

1. **Clone and build:**
```bash
git clone https://github.com/pgmi/pgmi.git
cd pgmi
go build -o bin/pgmi ./cmd/pgmi
```

2. **Set up environment variable:**
```bash
# Windows PowerShell
$env:PGMI_CONNECTION_STRING="postgresql://user:pass@localhost:5432/postgres"

# Windows CMD
set PGMI_CONNECTION_STRING=postgresql://user:pass@localhost:5432/postgres

# Linux/Mac
export PGMI_CONNECTION_STRING="postgresql://user:pass@localhost:5432/postgres"
```

Or copy `.env.example` to `.env` and customize:
```bash
cp .env.example .env
# Edit .env with your connection details
```

## Test the Example Project

The repository includes an example project you can use to test pgmi:

```bash
# Create a new database (with overwrite protection via countdown)
./bin/pgmi deploy ./example-project --db pgmi_test --overwrite --force --verbose

# Deploy without overwriting (will fail if database exists)
./bin/pgmi deploy ./example-project --db pgmi_test --verbose

# Deploy with parameters
./bin/pgmi deploy ./example-project \
  --db pgmi_test \
  --param env=development \
  --param api_key=secret123 \
  --verbose
```

## Create Your Own Project

1. **Initialize a new project:**
```bash
./bin/pgmi init my-project --template basic
```

This generates a `pgmi.yaml` with connection defaults, enabling `pgmi deploy .` with no extra flags.

2. **Customize your project:**
- Edit `my-project/deploy.sql` to implement deployment logic
- Add SQL files to `my-project/migrations/`
- Implement checksum tracking, parameter substitution, etc.

3. **Deploy:**
```bash
./bin/pgmi deploy ./my-project --db my_database --overwrite --force
```

## Project Structure

```
my-project/
├── deploy.sql              # Your deployment orchestrator
├── pgmi.yaml               # Project configuration (connection defaults, parameters)
└── migrations/             # Your SQL files
    ├── 001_create_users.sql
    └── 002_create_posts.sql
```

## Available Commands

- `pgmi deploy <path>` - Deploy a project
- `pgmi init <name>` - Initialize new project (coming soon)
- `pgmi --help` - Show help

## Key Concepts

### deploy.sql
Your `deploy.sql` script has full control. It has access to:

**pgmi_files table:**
- `path`, `name`, `content`, `checksum`, `size_bytes`, `modified_at`

**pgmi_params table:**
- `key`, `value`

### Example deploy.sql Logic

```sql
DO $$
DECLARE
    sql_file RECORD;
BEGIN
    -- Create migrations tracking table
    CREATE TABLE IF NOT EXISTS schema_migrations (
        checksum TEXT PRIMARY KEY,
        executed_at TIMESTAMPTZ DEFAULT NOW()
    );

    -- Execute new migrations
    FOR sql_file IN
        SELECT * FROM pgmi_files
        WHERE checksum NOT IN (SELECT checksum FROM schema_migrations)
        ORDER BY path
    LOOP
        RAISE NOTICE 'Executing: %', sql_file.path;
        EXECUTE sql_file.content;
        INSERT INTO schema_migrations (checksum) VALUES (sql_file.checksum);
    END LOOP;
END;
$$;
```

## Next Steps

1. Read the [full documentation](CLAUDE.md)
2. Study the [example project](example-project/)
3. Review the [technical specification](MVP.md)
4. Build your deployment logic in `deploy.sql`

## Troubleshooting

**Connection refused:**
- Check your PostgreSQL is running
- Verify connection string format
- Test with `psql` first

**Permission denied:**
- User needs CREATE/DROP DATABASE privileges
- Use `--management-db` flag if needed

**Import errors:**
- Run `go mod tidy`
- Ensure Go 1.21+ is installed
