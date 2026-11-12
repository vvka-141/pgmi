pub(super) const ROOT: &str = "PostgreSQL-native deployment driver.

pgmi loads your project files and CLI parameters into pg_temp tables, then
runs deploy.sql against the target database. You write the deployment in SQL
and PL/pgSQL — transactions, ordering, idempotency, retries are under your
control. pgmi handles connection, parameters, and exit codes.

  pgmi init demo                Scaffold a starter project
  pgmi deploy ./demo -d mydb    Run deploy.sql against mydb
  pgmi help deploy              Full deploy options

Connection follows libpq: PGHOST, PGUSER, PGPASSWORD, PGDATABASE, PGSSLMODE,
.pgpass. Run `pgmi help deploy` for cloud-auth flags and exit codes.";

pub(super) const DEPLOY: &str = "Run deploy.sql against a target database.

pgmi connects, loads project files into pg_temp tables, loads CLI parameters,
then executes deploy.sql. Transactions, ordering, and idempotency are decided
by deploy.sql, not by pgmi.

  pgmi deploy . -d mydb
  pgmi deploy . -d mydb --overwrite --force
  pgmi deploy . -d mydb --params-file prod.params
  pgmi deploy . -d mydb --param env=prod --param version=1.2.3
  ./fetch-secrets | pgmi deploy . -d mydb --params-file -

Password is never read from a flag. Use $PGPASSWORD, .pgpass, or a connection
string. Cloud auth: --azure, --aws, --google, --databricks (no password needed).

A project .env is loaded into the environment first (variables already set
win); pgmi serve does not read it.

Parameter precedence: --param > --params-file (later wins) > PGMI_PARAM_*
environment variables > pgmi.yaml. PGMI_PARAM_DB_PASSWORD=x becomes
current_setting('pgmi.db_password'); other environment variables are not read.
Secrets go in the environment or a params file, never in --param.

Exit codes:
  0   success
  10  invalid configuration       13  SQL execution failed
  11  connection failed           14  deploy.sql not found
  12  user denied overwrite       15  concurrent deploy in progress
  16  timeout exceeded            130 interrupted (SIGINT, SIGTERM)
A --timeout that runs out while connecting is 11: the server was not reached.";

pub(super) const INIT: &str = "Scaffold a new pgmi project (deploy.sql, directory layout, README).

  pgmi init                  Scaffold in the current directory (wizard if TTY)
  pgmi init ./demo           Scaffold in ./demo
  pgmi init ./demo -t basic  Skip the wizard, use the basic template

Without a TTY and without -t, pgmi init uses the minimal template.

Templates:
  minimal   A deploy.sql loop, hello_world.sql and a .pgmiignore; nothing else
  basic     Two layers: run-once tables, re-run views and functions
  advanced  Reference app: SQL-native REST/RPC/MCP, multi-tenant RLS, audit

Target directory must be empty (pgmi.yaml, .env and .git are tolerated).
Run `pgmi templates list` for full template descriptions.";

pub(super) const VERSION: &str = "Print pgmi version, commit, build date, and platform to stdout.

The first line is greppable: `pgmi version | head -1` returns just the
version string (psql --version convention).";

pub(super) const INFO: &str = "Inspect a pgmi project directory without connecting to a database.

Shows file counts by directory, template type, deploy.sql presence,
test coverage, and metadata usage.";

pub(super) const METADATA: &str = "Inspect and scaffold <pgmi-meta> blocks in your SQL files.

  pgmi metadata scaffold ./project --write
  pgmi metadata validate ./project
  pgmi metadata plan ./project --json

All three subcommands operate purely on the filesystem — no database
connection is opened.";

pub(super) const METADATA_PLAN: &str =
    "Show, without a database, the rows pgmi_plan_view will hold at deploy time:
every loaded non-test file once per sort key (its path when it has none), in
execution order. Non-SQL files are listed and marked, as the view includes them.

  pgmi metadata plan ./project
  pgmi metadata plan ./project --json

Use this to verify ordering before running `pgmi deploy`.";

pub(super) const METADATA_VALIDATE: &str =
    "Check that every <pgmi-meta> block parses, conforms to the XSD schema,
and that no two files share an id.

  pgmi metadata validate ./project
  pgmi metadata validate ./project --json

Exit non-zero on any failure.";

pub(super) const METADATA_SCAFFOLD: &str =
    "Add <pgmi-meta> blocks to SQL files that don't have one. Each new block
gets a random id and the sort key <prefix>/<file name>.

The prefix places the file in the execution order, so it comes from you:
--sort-key, or else the prefix the files with metadata in the same directory
already share. A directory path cannot supply it: layers do not run in
alphabetical order (core before api), and an advanced project's keys belong
in its numbered bands.

  pgmi metadata scaffold ./project/core --sort-key 005/core/          Preview only
  pgmi metadata scaffold ./project/core --sort-key 005/core/ --write  Apply to files
  pgmi metadata scaffold ./project --write      Prefixes from existing metadata

Without --write, no files are modified.";

pub(super) const TEMPLATES: &str =
    "List or describe the project templates that `pgmi init` can scaffold.

  pgmi templates list
  pgmi templates describe basic";

pub(super) const TEMPLATES_LIST: &str =
    "Print the names and one-line descriptions of all bundled templates.";

pub(super) const TEMPLATES_DESCRIBE: &str =
    "Show the directory layout, features, and intended use of one template.";

pub(super) const AI: &str =
    "Print machine-readable pgmi documentation. Designed for AI coding assistants
(Claude Code, Copilot, Gemini, Cursor) to ingest as context.

  pgmi ai                       Overview and index (this is the entrypoint)
  pgmi ai contract              Session-API contract as JSON

Both print to stdout for piping. A project's own agent guidance
(AGENTS.md and .agents/skills/) is written by pgmi init.";

pub(super) const AI_CONTRACT: &str =
    "Print the pgmi session-API contract as JSON. Agents should query this
before writing SQL against pgmi views/functions to avoid hallucinating
identifiers.

Output includes: the stability promise (before 1.0 the contract may change
in a minor release; every change is listed under Upgrading in the release
notes), view names and columns, test function signatures, step types, exit
codes, and preprocessor macro forms.";

pub(super) const SERVE: &str = "Expose pgmi's commands as MCP tools over stdio (JSON-RPC 2.0).

MCP-capable assistants (Claude Code, OpenCode) can use pgmi natively instead of
spawning a subprocess and parsing text. The tools map 1:1 to existing CLI
commands — no new deployment semantics. deploy resolves its connection,
database, timeout and parameters like pgmi deploy: tool arguments, then the
server's environment, then the project's pgmi.yaml. The project .env is not
read. Nothing from one call is stored in server state.

Add it to Claude Code with:

  claude mcp add pgmi -- pgmi serve

Tools: deploy, init, metadata_plan, metadata_validate, templates_list,
ai_overview, ai_contract.

The server reads JSON-RPC from stdin and writes responses to stdout; all
diagnostics go to stderr. It exits cleanly on EOF or SIGINT.

A failing tool answers with isError and the session continues. A malformed
message gets a -32700 parse error with a null id and the session continues.
Tool calls run concurrently: ping is answered during a deploy, and
notifications/cancelled stops it. A deploy to a database another deploy
holds exits 15.";

pub(super) const CONFIG: &str = "Launch the connection wizard and write the result to pgmi.yaml.

  pgmi config              In the current directory
  pgmi config ./project    In ./project

The wizard handles local, Azure Entra ID, AWS IAM, and Google Cloud SQL
auth. Requires an interactive terminal — for CI, write pgmi.yaml by hand.";

pub(super) const COMPLETION: &str =
    "Print a shell completion script to stdout. Source it in your shell rc.

  pgmi completion bash       > /etc/bash_completion.d/pgmi
  pgmi completion zsh        > \"${fpath[1]}/_pgmi\"
  pgmi completion fish       > ~/.config/fish/completions/pgmi.fish
  pgmi completion powershell | Out-String | Invoke-Expression";

pub(super) const TEMPLATES_DESCRIBE_EXAMPLES: &str = "Examples:
  pgmi templates describe basic
  pgmi templates describe advanced";

pub(super) const HELP: &str = "Print the help for a command.

  pgmi help deploy
  pgmi help metadata scaffold";
