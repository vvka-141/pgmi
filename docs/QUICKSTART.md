---
title: "Quickstart"
description: "Install pgmi, scaffold a project, deploy to local PostgreSQL, and verify the result."
weight: 30
---

# Getting Started with pgmi

This guide takes you from zero to a working deployment in about 10 minutes. The commands are concrete — the only values you supply are your local PostgreSQL password and the database name.

**What you'll do:**
1. Install pgmi
2. Make sure PostgreSQL is reachable
3. Create a project
4. Deploy it
5. Verify the deployment

**What you need:**
- PostgreSQL 15 or newer (18 or newer for the advanced template) running on `localhost:5432` (the default), reachable over a **direct connection** or a session-mode pooler — transaction-mode poolers (PgBouncer txn mode, RDS Proxy) break pgmi's session temp tables; see [Connection Requirements](PRODUCTION.md#connection-requirements)
- A PostgreSQL user with database creation rights (typically `postgres`)

No Go toolchain required — the quickstart installs a prebuilt binary. (Installing from source with `go install` is an optional alternative; see Step 1.)

---

## Fastest path: five minutes with Docker

Have Docker but no PostgreSQL at hand? This detour needs nothing else — no passwords to remember, no server setup, disposable afterwards. Install pgmi (Step 1 below), then:

```bash
docker run -d --name pgmi-demo -e POSTGRES_PASSWORD=postgres -p 5434:5432 postgres:17-alpine
docker exec pgmi-demo timeout 60 sh -c 'until pg_isready -h 127.0.0.1 -q; do sleep 1; done'
```

If either command prints an error (say, the port is already in use), stop: run
`docker rm pgmi-demo` and pick another port, in both `-p` and the connection string.

```bash
export PGMI_CONNECTION_STRING="postgresql://postgres:postgres@127.0.0.1:5434/postgres"
# PowerShell: $env:PGMI_CONNECTION_STRING = "postgresql://postgres:postgres@127.0.0.1:5434/postgres"
pgmi init demo --template basic
pgmi deploy demo -d demo_db
```

pgmi creates the `demo_db` database, loads the project into a session, and your generated `deploy.sql` runs the project's SQL files and the test suite in the [atomic head](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract) of the script — everything up to the first top-level `COMMIT` is one transaction:

```text
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_function_privileges.sql
[pgmi] Test: ./physical/__test__/test_constraints.sql
[pgmi] Fixture: ./semantic/__test__/_setup.sql
[pgmi] Test: ./semantic/__test__/test_project.sql
[pgmi] Test: ./semantic/__test__/test_task.sql
[pgmi] Test suite passed
✓ demo_db: 14 files loaded, 2 paths ignored in 0.09s
```

Now see what makes this different from `psql -f`: make a test fail and watch the whole deployment refuse to commit. Edit `demo/semantic/__test__/test_task.sql`, add `RAISE EXCEPTION 'forced failure';` anywhere in the DO block, and redeploy:

```bash
pgmi deploy demo -d demo_db
```

pgmi exits with code `13`, and PostgreSQL rolls back the transactional schema and
data changes made before the failing test. Sequence counters and external or
explicitly non-transactional effects are not rolled back. That test gate, inside
your own deployment transaction, is the core of the pgmi model.

Clean up with `docker rm -f pgmi-demo`, and clear the connection string before continuing — it outranks the `pgmi.yaml` you are about to edit: `unset PGMI_CONNECTION_STRING` (PowerShell: `Remove-Item Env:PGMI_CONNECTION_STRING`). For a real setup against your own PostgreSQL server, continue below.

---

## Step 1: Install pgmi

Install the prebuilt binary — no Go toolchain needed.

**macOS / Linux:**
```bash
curl -sSL https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.sh | bash
```

**Windows (PowerShell):**
```powershell
irm https://raw.githubusercontent.com/vvka-141/pgmi/main/scripts/install.ps1 | iex
```

Prefer a package manager or a checksum-verified download? `brew install --cask vvka-141/pgmi/pgmi` (macOS), or a [GitHub Releases](https://github.com/vvka-141/pgmi/releases) archive or `.deb` package all work — see the [full install options](../README.md#install).

Verify the installation:

```bash
pgmi --version
```

You should see output like:

```
pgmi 0.x.x (compat 1)
Commit: <sha>, Built: <date>, Platform: <os>/<arch>
```

> **If `pgmi` is not found**, the install directory is not on your PATH. The install script prints the location it used; add that directory to your PATH and restart your terminal.

<details>
<summary>Install from source (optional — requires Go 1.26+)</summary>

```bash
go install github.com/vvka-141/pgmi/cmd/pgmi@latest
```

`pgmi --version` then reports `Commit: unknown, Built: unknown`: a module-proxy build carries no VCS stamp.

If `pgmi` is then not found, your Go bin directory is not on your PATH:

- **Linux/macOS**: add `export PATH="$PATH:$(go env GOPATH)/bin"` to your `~/.bashrc` or `~/.zshrc`, then restart your terminal.
- **Windows PowerShell**: add `$env:Path += ";$(go env GOPATH)\bin"`, or set it permanently via System Settings → Environment Variables.

Run `go env GOPATH` to see where Go installs binaries; `pgmi` is in the `bin` subfolder.

</details>

**Alternative installation methods:**

<details>
<summary>macOS (Homebrew)</summary>

```bash
brew install --cask vvka-141/pgmi/pgmi
```
</details>

<details>
<summary>Debian/Ubuntu (.deb)</summary>

Download the `.deb` for your architecture from [GitHub Releases](https://github.com/vvka-141/pgmi/releases), check it against `checksums.txt`, and install it:

```bash
sha256sum --ignore-missing -c checksums.txt
sudo apt install ./pgmi_<version>_linux_amd64.deb
```

There is no APT repository, so `apt upgrade` does not pick up new releases; install the next `.deb` the same way.
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

> **Interactive setup**: if you run `pgmi init myapp` (without flags) on a TTY, an interactive wizard walks you through template choice and connection setup, saving a `pgmi.yaml` for later `pgmi deploy` runs. Add `--template <name>` to skip the template prompt, or set `PGMI_NON_INTERACTIVE=1` / `CI=1` to bypass wizards entirely.
>
> If you are using the advanced template (or adopting metadata-driven ordering) and already have SQL files without `<pgmi-meta>` blocks, run `pgmi metadata scaffold <dir> --sort-key <prefix>` per directory (e.g. `pgmi metadata scaffold ./core --sort-key 005/core/`) to generate them. Basic template users can ignore this.

This creates a ready-to-deploy project:

```
myapp/
├── deploy.sql              ← Your deployment logic (the brain)
├── project.json            ← Project metadata (read by deploy.sql, not by pgmi)
├── pgmi.yaml               ← Connection defaults (the config)
├── physical/               ← Tables, keys, indexes: each file runs once
│   ├── 001_project.sql
│   ├── 002_task.sql
│   └── __test__/           ← Tests of the constraints
├── semantic/               ← Views and functions: re-run on every deploy
│   ├── project.sql
│   ├── task.sql
│   └── __test__/           ← Fixture and tests of the writers (or __tests__/)
├── __test__/
│   └── test_function_privileges.sql
├── README.md
└── .gitignore
```

> **Note:** Both `__test__/` and `__tests__/` work identically. Use whichever matches your team's convention.

Let's look at what was generated.

### pgmi.yaml — your project config

Open `pgmi.yaml`. It looks like this:

```yaml
# pgmi project configuration
# CLI flags override these values. Environment variables (PGHOST, etc.) also apply.

connection:
  database: mydb              # Target database (change this!)
  host: localhost             # PostgreSQL host
  port: 5432                  # PostgreSQL port
  sslmode: prefer             # SSL mode: disable, allow, prefer, require, verify-ca, verify-full
  # username: postgres        # PostgreSQL user (default: current OS user)

# Parameters passed to deploy.sql (accessible via current_setting('pgmi.key', true))
params:
  env: dev

timeout: 3m                   # Deployment timeout
```

This tells pgmi: "when I deploy, connect to `localhost:5432` and use a database called `mydb`". No JSON, no XML — just this small YAML file for connection defaults.

**Update the database name and add your username:**

```yaml
connection:
  host: localhost
  port: 5432
  username: postgres
  database: myapp

params:
  env: dev

timeout: 3m
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
> Alternatively, use a full connection string. Name the **target** database in
> it — `myapp`, not `postgres`:
> ```bash
> # Linux/macOS
> export PGMI_CONNECTION_STRING="postgresql://postgres:your-postgres-password@localhost:5432/myapp"
>
> # Windows PowerShell
> $env:PGMI_CONNECTION_STRING="postgresql://postgres:your-postgres-password@localhost:5432/myapp"
> ```
>
> `PGMI_CONNECTION_STRING` is an **environment** source, and environment beats
> `pgmi.yaml` in the [precedence chain](CONFIGURATION.md#precedence-chain). It
> replaces the whole `connection:` block — host and port as well as the
> database — it does not complement it: end the
> string in `/postgres` and pgmi deploys into `postgres` — creating your tables
> in the maintenance database and never creating `myapp` at all.
>
> If you would rather keep `/postgres` in the string, say the target on the
> command line, which outranks both: `pgmi deploy . -d myapp`.

### deploy.sql — your deployment logic

Open `deploy.sql`. This is the only file that controls what happens during deployment. Not a config file. Not a framework. Just PostgreSQL — the templates use PL/pgSQL, PostgreSQL's procedural language, for loops and conditionals.

pgmi loads your project files into session-scoped views, then runs `deploy.sql`:
- **`pg_temp.pgmi_source_view`** — raw access to all files (for introspection)
- **`pg_temp.pgmi_plan_view`** — files sorted by execution order (for deployment)

Both templates run the files from `pg_temp.pgmi_plan_view`. The basic template's `deploy.sql` also keeps a small ledger, `deployment.run_once`, of the files marked run-once, and refuses a deploy when one of them changed after it ran.

### physical/ and semantic/ — two layers

`physical/` holds the tables, keys, constraints and indexes. Each file opens with a `<pgmi-meta ... idempotent="false">` header: it runs once per database and is frozen after that. `semantic/` holds the views (the read surface) and the functions that are the only writers; those files carry no header and re-run on every deploy, so they use `CREATE OR REPLACE`. The sample is a small project and task model: `fn_create_project`, `fn_add_task` and `fn_complete_task` write, `project_view` reads, and `fn_project_problems`, `fn_task_problems` and `fn_task_problem` say why a write would be refused.

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

**Note:** `--overwrite` is for local development. In production, deploy incrementally without this flag. pgmi drops and recreates the database *before* `deploy.sql` runs, so a deploy that then fails (for example on a missing parameter) leaves an empty database behind. With the advanced template, always pass `--param env=dev` on a local `--overwrite`.

> **CI/CD:** pin the pgmi binary version (before 1.0 the session API may change in a minor release, listed under Upgrading in the release notes), add `--compat 1`, and drop `--overwrite` for incremental deploys:
> ```bash
> pgmi deploy . -d myapp --compat 1 --force --param env=prod
> ```
> See the [CI/CD Guide](CICD.md) for a complete pipeline example.

You should see output including test notices and ending with an ASCII art "DONE" banner:

```
[dev] Deploying myapp v1.0.0
[pgmi] Test suite started
[pgmi] Test: ./__test__/test_function_privileges.sql
[pgmi] Test: ./physical/__test__/test_constraints.sql
[pgmi] Fixture: ./semantic/__test__/_setup.sql
[pgmi] Test: ./semantic/__test__/test_project.sql
[pgmi] Test: ./semantic/__test__/test_task.sql
[pgmi] Test suite passed

  ___   ___  _  _ ___
 |   \ / _ \| \| | __|
 | |) | (_) | .` | _|
 |___/ \___/|_|\_|___|
```

Your database `myapp` now exists with `project` and `task` tables, a view, the writer functions and a `Demo` project, and the built-in tests have already verified them.

---

## Step 5: Verify the deployment

### Using pgAdmin

1. In the left panel, right-click "Databases" → Refresh
2. You should see a new database called **myapp**
3. Open the Query Tool (Tools → Query Tool) and run:

```sql
SELECT name, open_task_count, state FROM project_view;
```

You should see the `Demo` project, with no tasks, in state `empty`.

### Using the terminal

```bash
psql -h localhost -U postgres -d myapp -c "SELECT name, open_task_count, state FROM project_view;"
```

```
 name | open_task_count | state
------+-----------------+-------
 Demo |               0 | empty
```

Add a task through the writer and read the view again:

```bash
psql -h localhost -U postgres -d myapp \
  -c "SELECT title, status FROM fn_add_task((SELECT project_id FROM project WHERE name = 'Demo'), 'Write the plan');" \
  -c "SELECT name, open_task_count, state FROM project_view;"
```

The project is now `active`. Call `fn_add_task` again with the same title and it returns NULL, shown as a row whose columns are all empty: an open task of the project already has it. `SELECT * FROM fn_task_problems(<project_id>, 'Write the plan')` says why (`title`, `duplicate`).

You just deployed a PostgreSQL project with pgmi.

---

## Step 6: Change the schema

Add a column to the tasks. Treat `myapp` as a database that keeps its data: add `due_on date,` to the `CREATE TABLE task` in `physical/002_task.sql` and deploy without `--overwrite`:

```bash
pgmi deploy . --force
```

The deploy fails before anything runs:

```
Run-once file(s) changed after they ran on this database:
  ./physical/002_task.sql: content edited
```

That file already ran here and is never run again, so the edit would only ever reach fresh databases. Undo the edit and put the change in a new run-once file instead, `physical/003_task_due_on.sql`, with a fresh id from `uuidgen`:

```sql
/*
<pgmi-meta id="<your new uuid>" idempotent="false">
  <description>Tasks get a due date</description>
</pgmi-meta>
*/
ALTER TABLE task ADD COLUMN due_on date;
```

A file in `physical/` without that header would run again on every deploy, so the deploy refuses it, naming the file. `pgmi deploy . --force` now runs the new file once (and re-runs `semantic/`, as always), and the ledger records it:

```bash
psql -h localhost -U postgres -d myapp -c "SELECT path, ran_at FROM deployment.run_once ORDER BY ran_at;"
```

While a database can still be thrown away, keeping the edit in `002_task.sql` and recreating it with `pgmi deploy . --overwrite --force` is fine too. Views and functions in `semantic/` need none of this: edit them and redeploy, they re-run every time. The exception is a change `CREATE OR REPLACE` refuses (dropping, renaming or reordering a view's columns, changing a function's return type): drop the object first in the same file, as the `pgmi-layers` skill shows.

Now watch a test gate the deploy. Add `RAISE EXCEPTION 'forced failure';` inside the DO block in `semantic/__test__/test_task.sql`, then run `pgmi deploy . --force`. pgmi exits with code `13`, and the transaction that held this run's changes rolls back. Remove the line before you continue.

> **Where the atomic boundary is.** The generated `deploy.sql` has a `COMMIT` after the test gate (the `COMMIT;` right after `ROLLBACK TO SAVEPOINT _tests`). Everything you add *above* it shares the test-gated transaction; anything *below* it autocommits statement by statement. If you need `CREATE INDEX CONCURRENTLY` or another command that cannot run inside a transaction, put it after the `COMMIT`. See [the execution contract](DEPLOY-GUIDE.md#atomic-mode-then-psql-mode-the-execution-contract) for the full rules.

---

## What just happened?

Here's the entire model in four points:

1. **pgmi loaded your files** (everything in the project folder) into PostgreSQL temporary tables and views (`pg_temp.pgmi_source_view`, `pg_temp.pgmi_plan_view`)
2. **pgmi ran `deploy.sql`**, which queries those views and directly executes files using `EXECUTE v_file.content`
3. **Your SQL files are regular SQL** — no framework conventions, no special syntax
4. **`deploy.sql` is the only thing that decides** what runs, in what order, with what transaction boundaries. Not a config file. Not pgmi. Your SQL.

This is what makes pgmi different from migration tools: PostgreSQL itself is the deployment engine. You write the logic in SQL, and pgmi just provides the infrastructure to get your files into the database session.

---

## Choosing a template

pgmi provides two templates for `pgmi init`. Start with **basic** when you want a small two-layer project: run-once tables, re-run views and functions. Reach for **advanced** when you want a large, editable reference system — metadata tracking, roles, schemas, audit logging, and API/MCP patterns you adapt to your needs. Advanced is not a "more production" version of basic; it's a different, richer starting point you own and trim. **Either can be production** depending on the project — advanced is _more complete_, not _more production_.

| Feature | Basic | Advanced |
|---------|-------|----------|
| **Learning curve** | Minimal — read the code, understand it | Moderate — more moving parts |
| **File ordering** | Path-based (`physical/` before `semantic/`) | Metadata-driven via `<pgmi-meta>` sort keys |
| **Execution view** | `pg_temp.pgmi_plan_view` | `pg_temp.pgmi_plan_view` (with multi-phase support) |
| **Idempotency control** | `idempotent="false"` header on run-once files; the rest re-runs | Metadata-driven (`idempotent="true/false"`), with a file-contract check |
| **Script tracking** | `deployment.run_once` ledger; an edited, moved or removed run-once file refuses the deploy | UUID-based tracking in `internal.deployment_script_execution_log`, same refusal |
| **Testing** | `CALL pgmi_test()` with savepoints | Same, plus hierarchical fixtures |
| **Project structure** | Two layers: `physical/`, `semantic/`, each with `__test__/` | Multi-module: `api/`, `lib/` (core, common, api), `membership/`, `gateways/` |
| **Parameters** | `current_setting('pgmi.key', true)` | Same, plus `deployment_setting()` helper with defaults |
| **MCP integration** | None | Full MCP server for AI assistants |

**When to use basic:**
- Learning pgmi
- Simple projects with < 20 SQL files
- Two layers in path order, without phases or roles

**When to use advanced:**
- You want pre-built patterns for script tracking, idempotency, role separation, audit logging, or API/MCP integration — and you're comfortable adapting or removing what doesn't apply
- Large projects with explicit execution phases
- Teams that benefit from script tracking and audit logging
- Projects integrating with AI assistants via MCP

**Advanced template requirements:**
- PostgreSQL 18+ (`deploy.sql` refuses an older server before creating anything)
- A role with `CREATEROLE` + `CREATEDB` and `createrole_self_grant = 'set, inherit'` set on it — **no superuser** (it creates the owner, admin, api and kernel roles, and the setting lets it use them).
- Extensions: `uuid-ossp`, `pgcrypto`, `pg_trgm`, `hstore` (available on the managed clouds that offer PostgreSQL 18 — see the [Production Guide](PRODUCTION.md#managed-cloud-postgresql))

**Switching templates:**

There is no in-place switch: the advanced template is a different application with its own deploy.sql, ledger and file contract. To move a basic project:
1. Scaffold the advanced template into a new directory.
2. Copy `physical/` files in as run-once files and `semantic/` files as re-run files. They already carry `<pgmi-meta>` ids; give them sort keys at `005/` or later with `pgmi metadata scaffold <dir> --sort-key 005/<entity>/ --write`, and keep each file to what the advanced README's file contract allows.
3. Deploy into a new database and move the data with your own SQL. The basic `deployment.run_once` ledger does not carry over.

See [Metadata Guide](METADATA.md) for sort keys.

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
| Deploy from a CI/CD pipeline | [CI/CD Guide](CICD.md) |
| Handle secrets in CI/CD | [Security Guide](SECURITY.md) |

---

## Troubleshooting

### "pgmi: command not found"

The directory pgmi was installed into is not on your PATH:

- **install.sh**: `/usr/local/bin`, or whatever you set `INSTALL_DIR` to.
- **install.ps1**: `%LOCALAPPDATA%\pgmi`, or `PGMI_INSTALL_DIR` if you set it. The script adds it to your user PATH; terminals that were already open need a restart.
- **go install**: `$(go env GOPATH)/bin` — see [Step 1](#step-1-install-pgmi).

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
