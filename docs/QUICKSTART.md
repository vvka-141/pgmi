# Getting Started with pgmi

This guide takes you from zero to a working deployment in about 10 minutes. Every command is copy-paste ready — no placeholders to fill in.

**What you'll do:**
1. Install pgmi
2. Make sure PostgreSQL is reachable
3. Create a project
4. Deploy it
5. Verify the deployment

**What you need:**
- PostgreSQL running on `localhost:5432` (the default)
- A PostgreSQL user with database creation rights (typically `postgres`)
- Go 1.22+ installed ([download here](https://go.dev/dl/))

---

## Step 1: Install pgmi

The recommended way is `go install`. This downloads the pgmi source code, compiles it into a binary, and places it in your Go bin directory — making it available as a command on your machine, just like any other installed program.

```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

Verify the installation:

```bash
pgmi --version
```

You should see output like:

```
pgmi version 0.x.x
```

> **If `pgmi` is not found**, your Go bin directory is not in your PATH. Add it:
>
> - **Linux/macOS**: Add `export PATH="$PATH:$(go env GOPATH)/bin"` to your `~/.bashrc` or `~/.zshrc`, then restart your terminal.
> - **Windows PowerShell**: Add `$env:Path += ";$(go env GOPATH)\bin"` or add the path permanently through System Settings → Environment Variables.
>
> Run `go env GOPATH` to see where Go installs binaries. The `pgmi` executable will be in the `bin` subfolder of that directory.

**Alternative installation methods:**

<details>
<summary>macOS (Homebrew)</summary>

```bash
brew tap vvka-141/pgmi
brew install pgmi
```
</details>

<details>
<summary>Debian/Ubuntu (APT)</summary>

```bash
curl -1sLf 'https://dl.cloudsmith.io/public/vvka-141/pgmi/setup.deb.sh' | sudo bash
sudo apt update && sudo apt install pgmi
```
</details>

<details>
<summary>Windows (direct download)</summary>

Download the latest `.zip` from [GitHub Releases](https://github.com/vvka-141/pgmi/releases), extract it, and add the folder to your PATH.
</details>

---

## Step 2: Make sure PostgreSQL is reachable

pgmi needs to connect to PostgreSQL. Let's verify your server is running and accepting connections.

**Using pgAdmin**:

1. Open pgAdmin
2. In the left panel, you should see a server (usually called "PostgreSQL" or "Local")
3. Click on it — if it connects without errors, you're good
4. Note the connection details: right-click the server → Properties → Connection tab. You'll see the host, port, and username

**Using the terminal**:

```bash
psql -h localhost -U postgres -c "SELECT version();"
```

If prompted for a password, enter your `postgres` user password. You should see something like:

```
                          version
------------------------------------------------------------
 PostgreSQL 16.x on x86_64-...
```

> **Common issues:**
> - **"Connection refused"**: PostgreSQL is not running. Start it via your OS service manager, or open pgAdmin — it often shows a clear error about the server being down.
> - **"Password authentication failed"**: Wrong password. If you just installed PostgreSQL, the default password is whatever you set during installation. On some Linux installs, local connections use `peer` authentication — try `sudo -u postgres psql` instead.
> - **"Could not connect to server"**: Check that PostgreSQL is listening on `localhost:5432`. In pgAdmin: right-click server → Properties → Connection tab.

---

## Step 3: Create a project

```bash
pgmi init myapp --template basic
cd myapp
```

This creates a ready-to-deploy project:

```
myapp/
├── deploy.sql              ← Your deployment logic (the brain)
├── pgmi.yaml               ← Connection defaults (the config)
├── migrations/             ← Your SQL files go here
│   └── 001_hello_world.sql
├── __test__/               ← Your test files
│   └── test_hello_world.sql
└── README.md
```

Let's look at what was generated.

### pgmi.yaml — your project config

Open `pgmi.yaml`. It looks like this:

```yaml
connection:
  database: myapp

params:
  env: development
```

This tells pgmi: "when I deploy, create/connect to a database called `myapp`". That's it. No JSON, no XML — just this small YAML file for connection defaults.

**Update it for your local PostgreSQL:**

```yaml
connection:
  host: localhost
  port: 5432
  username: postgres
  database: myapp

params:
  env: development
```

> **Where does the password go?** Not in `pgmi.yaml` — that file is meant to be committed to Git. Set the password as an environment variable instead:
>
> ```bash
> # Linux/macOS
> export PGPASSWORD="your-postgres-password"
>
> # Windows PowerShell
> $env:PGPASSWORD="your-postgres-password"
>
> # Windows CMD
> set PGPASSWORD=your-postgres-password
> ```
>
> Alternatively, use a full connection string:
> ```bash
> # Linux/macOS
> export PGMI_CONNECTION_STRING="postgresql://postgres:your-postgres-password@localhost:5432/postgres"
>
> # Windows PowerShell
> $env:PGMI_CONNECTION_STRING="postgresql://postgres:your-postgres-password@localhost:5432/postgres"
> ```
>
> When `PGMI_CONNECTION_STRING` is set, it provides the host, port, username, and password. pgmi connects to the database in the connection string first (typically `postgres`) to run `CREATE DATABASE`, then switches to the target database from `pgmi.yaml` to deploy. This is why the connection string points to `postgres` while `pgmi.yaml` says `myapp` — they serve different purposes.

### deploy.sql — your deployment logic

Open `deploy.sql`. This is the only file that controls what happens during deployment. Not a config file. Not a framework. Just PostgreSQL — the templates use PL/pgSQL, PostgreSQL's procedural language, for loops and conditionals.

pgmi loads all your project files into a temporary table called `pg_temp.pgmi_source`, then runs `deploy.sql`. Your job in `deploy.sql` is to decide which files to execute and in what order, by calling `pgmi_plan_*` functions that build a command queue—pgmi runs it afterward.

### migrations/001_hello_world.sql — your first SQL file

This is a regular SQL file. Nothing special about it — no annotations required, no magic comments. It creates a `hello_world()` function that demonstrates pgmi's parameter system.

---

## Step 4: Deploy

Make sure your password is set (see Step 3), then:

```bash
pgmi deploy . --overwrite --force
```

What this does:
- `.` means "this directory" (where pgmi.yaml and deploy.sql are)
- `--overwrite` allows dropping and recreating the database if it already exists
- `--force` replaces interactive confirmation with a 5-second countdown (you can still press Ctrl+C to cancel)

**Note:** `--overwrite` is for local development. In production, deploy incrementally without this flag.

You should see output ending with:

```
✓ Migrations complete
✓ All tests passed (no side effects)
✓ Deployment complete!
```

Your database `myapp` now exists with the `hello_world()` function and the built-in test has already verified it works.

---

## Step 5: Verify the deployment

### Using pgAdmin

1. In the left panel, right-click "Databases" → Refresh
2. You should see a new database called **myapp**
3. Open the Query Tool (Tools → Query Tool) and run:

```sql
SELECT hello_world();
```

You should see: `Hello, World!`

### Using the terminal

```bash
psql -h localhost -U postgres -d myapp -c "SELECT hello_world();"
```

```
  hello_world
---------------
 Hello, World!
```

Try it with a custom parameter:

```bash
pgmi deploy . --overwrite --force --param name=Alice
psql -h localhost -U postgres -d myapp -c "SELECT hello_world();"
```

```
   hello_world
-----------------
 Hello, Alice!
```

You just deployed a PostgreSQL project with pgmi.

---

## Step 6: Add a second migration

Create a new file `migrations/002_users.sql`:

```sql
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT
);
```

Deploy again:

```bash
pgmi deploy . --overwrite --force
```

Check the database (pgAdmin: refresh, or terminal):

```bash
psql -h localhost -U postgres -d myapp -c "\dt"
```

You should see your new `users` table. The basic template uses `CREATE OR REPLACE` / `IF NOT EXISTS` patterns, so you can also deploy without `--overwrite` for incremental changes — though during early development, `--overwrite --force` is the simplest approach.

---

## What just happened?

Here's the entire model in five points:

1. **pgmi loaded your files** (everything in the project folder) into a PostgreSQL temporary table called `pg_temp.pgmi_source`
2. **pgmi ran `deploy.sql`**, which read those files and built an execution plan (a queue of SQL commands)
3. **pgmi executed the plan** — running each queued command in order
4. **Your SQL files are regular SQL** — no framework magic, no special syntax
5. **`deploy.sql` is the only thing that decides** what runs, in what order, with what transaction boundaries. Not a config file. Not pgmi. Your SQL.

This is what makes pgmi different from migration tools: PostgreSQL itself is the deployment engine. You write the logic in SQL, and pgmi just provides the infrastructure to get your files into the database session.

---

## Next steps

Now that you have a working project:

| Want to... | Read |
|-----------|------|
| Understand when pgmi makes sense | [Why pgmi?](WHY-PGMI.md) |
| Migrate from Flyway or Liquibase | [Coming from Other Tools](COMING-FROM.md) |
| Pass parameters to your deployment | [Configuration Reference](CONFIGURATION.md) |
| Write database tests that run inside transactions | [Testing Guide](TESTING.md) |
| Understand the session tables and helper functions | [Session API Reference](session-api.md) |
| Use metadata for script tracking and ordering | [Metadata Guide](METADATA.md) |
| Prepare for production deployment | [Production Guide](PRODUCTION.md) |
| Handle secrets in CI/CD | [Security Guide](SECURITY.md) |

---

## Troubleshooting

### "pgmi: command not found"

Your Go bin directory is not in your PATH. See [Step 1](#step-1-install-pgmi) for instructions.

### "connection refused" or "could not connect to server"

PostgreSQL is not running or not listening on the expected host/port. Verify in pgAdmin (see [Step 2](#step-2-make-sure-postgresql-is-reachable)) or check your OS service manager.

### "password authentication failed"

The password you set via `PGPASSWORD` or `PGMI_CONNECTION_STRING` doesn't match your PostgreSQL user. Try connecting with pgAdmin or `psql` first to confirm the correct password.

### "database already exists" (without --overwrite)

Without `--overwrite`, pgmi creates the database if it doesn't exist and deploys to it if it does — this is normal incremental deployment. The `--overwrite` flag is only needed when you want to **drop and recreate** the database from scratch (local development, CI).

### "deploy.sql not found"

You're running `pgmi deploy` from the wrong directory. Make sure you're inside your project folder (where `deploy.sql` is), or pass the path explicitly: `pgmi deploy ./myapp`.

### "permission denied" when creating database

Your PostgreSQL user needs the `CREATEDB` privilege. In pgAdmin: right-click your login role → Properties → Privileges → check "Can create databases". Or via SQL:

```sql
ALTER ROLE postgres CREATEDB;
```
