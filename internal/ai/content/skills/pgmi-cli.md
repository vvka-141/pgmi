---
name: pgmi-cli
description: "Use when adding CLI commands or flags"
user_invocable: true
---


**Use this skill when:**
- Adding new CLI commands or subcommands
- Adding or modifying CLI flags
- Understanding CLI design philosophy
- Working with Cobra command structure
- Implementing parameter validation
- Understanding the Approver interface
- Debugging CLI parsing or behavior

---

## CLI Design Philosophy

### Core Principle: Infrastructure, Not Orchestration

**pgmi CLI provides flags for infrastructure concerns ONLY, never deployment orchestration.**

This separation is fundamental to pgmi's architecture:
- **Infrastructure** = What to connect to, what parameters to pass
- **Orchestration** = How to deploy, when to rollback, what order

**The Divide:**

✅ **Valid CLI Concerns (Infrastructure):**
- Connection parameters (`--connection`, `--host`, `--port`, `--username`, `--database`)
- Authentication credentials (via connection string or environment variables)
- Parameter injection (`--param key=value`, `--params-file`)
- Safety workflows (`--overwrite`, `--force` for destructive operations)
- Observability (`--verbose`, `--json` output)
- Catastrophic failure protection (`--timeout` as safety net)

❌ **Invalid CLI Concerns (Orchestration - Belongs in deploy.sql):**
- Transaction control (`--no-transaction`, `--single-transaction`)
- Phase skipping (`--skip-migrations`, `--skip-tests`)
- Execution order control (`--reverse`, `--from-migration`)
- Retry behavior (`--retry-count`, `--retry-delay`)
- Idempotency enforcement (`--force-rerun`, `--skip-if-exists`)

**Rationale:**

When deployment logic lives in CLI:
- ❌ User loses control (tool makes decisions)
- ❌ Difficult to customize (need new flags for every scenario)
- ❌ Opinionated (tool imposes workflow)
- ❌ Hard to debug (logic hidden in tool)
- ❌ Not AI-friendly (implicit behavior, hidden state)

When deployment logic lives in deploy.sql:
- ✅ User has full control (SQL drives everything)
- ✅ Infinitely customizable (just write SQL)
- ✅ Unopinionated (tool provides infrastructure only)
- ✅ Easy to debug (all logic visible in SQL)
- ✅ AI-friendly (deterministic, inspectable)

**Example: Transaction Control**

❌ **BAD (CLI flag):**
```bash
pgmi deploy ./migrations --single-transaction
# Tool decides transaction strategy
```

✅ **GOOD (deploy.sql):**
```sql
-- User controls transaction strategy explicitly at top level
BEGIN;
DO $$
DECLARE v_file RECORD;
BEGIN
    FOR v_file IN (SELECT content FROM pg_temp.pgmi_plan_view ORDER BY execution_order) LOOP
        EXECUTE v_file.content;
    END LOOP;
END $$;
COMMIT;
```

**Example: Phase Skipping**

❌ **BAD (CLI flag):**
```bash
pgmi deploy ./migrations --skip-tests
# Tool decides what to skip
```

✅ **GOOD (deploy.sql):**
```sql
-- User controls what executes via parameter
IF COALESCE(current_setting('pgmi.run_tests', true), 'true') = 'true' THEN
    CALL pgmi_test();  -- preprocessor macro
END IF;
```

---

## Timeout Flag Design

### Philosophy: Catastrophic Failure Protection

**The `--timeout` flag (default: 3 minutes) is a safety net, NOT a deployment control.**

**Purpose:**
- Prevent indefinite hangs due to network issues
- Prevent indefinite hangs due to deadlocks
- Prevent indefinite hangs due to runaway queries
- Force developers to notice deployment issues quickly

**NOT for:**
- ❌ Controlling deployment duration
- ❌ Enforcing time limits on valid operations
- ❌ Replacing PostgreSQL's native timeouts

**Design Rationale:**

**Default is Short (3m) by Design:**
- Forces developers to notice deployment issues quickly
- Long-running deployments should be explicit (`--timeout 30m`)
- Prevents "fire and forget" + indefinite hangs

**PostgreSQL's Native Timeouts Remain Primary:**
```sql
-- Users control timeout behavior in deploy.sql
SET statement_timeout = '5min';   -- Per-statement limit
SET lock_timeout = '10s';          -- Lock acquisition limit
```

**Usage Patterns:**

**Development:**
```bash
# Short timeout (default) catches issues fast
pgmi deploy ./migrations -d mydb

# Deployment completes in 30 seconds? Perfect.
# Deployment hangs at 3 minutes? Something's wrong, investigate.
```

**Production:**
```bash
# Explicit timeout for known long-running deployments
pgmi deploy ./migrations -d mydb --timeout 30m

# Team knows deployment takes 15 minutes, so 30m is safe buffer
```

**Key Insight:**
If you're frequently increasing `--timeout`, investigate why deployments are slow (missing indexes, heavyweight DDL, lock contention) rather than just accepting longer times.

---

## Project Structure

### Command Organization

```
cmd/pgmi/
├── main.go                    # CLI entrypoint

internal/cli/
├── root.go                    # Root command setup
├── deploy.go                  # pgmi deploy command
├── init.go                    # pgmi init command
├── templates.go               # pgmi templates command (list, describe)
└── version.go                 # pgmi version command
```

### Cobra Framework

pgmi uses [Cobra](https://github.com/spf13/cobra) for CLI structure:

**Why Cobra?**
- ✅ Standard in Go ecosystem (used by kubectl, docker, gh, etc.)
- ✅ Rich flag parsing (local, persistent, required)
- ✅ Subcommand support
- ✅ Help generation
- ✅ Shell completion
- ✅ Well-documented

**Root Command Setup:**
```go
// internal/cli/root.go
var rootCmd = &cobra.Command{
    Use:   "pgmi",
    Short: "PostgreSQL-native execution fabric",
    Long:  `pgmi is a PostgreSQL-native deployment tool...`,
}

func Execute() error {
    return rootCmd.Execute()
}

func init() {
    // Add subcommands
    rootCmd.AddCommand(deployCmd)
    rootCmd.AddCommand(initCmd)
    rootCmd.AddCommand(templatesCmd)
    rootCmd.AddCommand(versionCmd)

    // Global flags (persistent across all commands)
    rootCmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "Verbose output")
}
```

---

## Commands Reference

### deploy Command

**Purpose:** Deploy SQL files to PostgreSQL database

**Signature:**
```bash
pgmi deploy <path> [flags]
```

**Flags:**
```
    --connection string     Connection string (PostgreSQL URI or ADO.NET)
-d, --database string       Target database (creates if doesn't exist)
    --param stringArray     Parameters (key=value)
    --params-file string    Load parameters from .env files (repeatable)
    --overwrite             Drop and recreate database if exists
    --force                 Skip confirmation prompts
    --timeout duration      Deployment timeout (default 3m)
-v, --verbose               Verbose output (also sets PostgreSQL client_min_messages to 'debug',
                            enabling RAISE DEBUG messages from SQL scripts)
```

**Examples:**
```bash
# Basic deployment
pgmi deploy ./migrations

# Create database and deploy
pgmi deploy ./migrations -d mydb --overwrite --force

# With parameters
pgmi deploy ./migrations \
  --param env=production \
  --param admin_password=secret

# Explicit connection string
pgmi deploy ./migrations \
  --connection "postgresql://postgres:password@localhost:5432/mydb"

# Long-running deployment
pgmi deploy ./migrations -d mydb --timeout 30m
```

**Implementation:** `internal/cli/deploy.go`

---

### init Command

**Purpose:** Scaffold new pgmi project from template

**Signature:**
```bash
pgmi init <project-name> [flags]
```

**Flags:**
```
-t, --template string       Template name (default: basic)
-v, --verbose               Verbose output
```

**Examples:**
```bash
# Create basic project
pgmi init myapp

# Create advanced project
pgmi init myapp --template advanced

# Verbose output
pgmi init myapp --template advanced --verbose
```

**Behavior:**
1. Validates project name (directory must be empty or not exist)
2. Creates directory if needed
3. Copies template files with `{{PROJECT_NAME}}` substitution
4. Displays success message with tree view

**Implementation:** `internal/cli/init.go`

---

### templates Command

**Purpose:** List and describe available templates

**Subcommands:**
```bash
pgmi templates list              # List all templates
pgmi templates describe <name>   # Describe specific template
```

**Examples:**
```bash
# List templates
$ pgmi templates list
Available templates:
  basic      Simple structure for learning
  advanced   Production-ready metadata-driven deployment

# Describe template
$ pgmi templates describe advanced
Template: advanced
Description: Production-ready template with metadata-driven deployment
Best For: Production databases, complex deployments

Features:
  - Metadata-driven execution (topological sort)
  - UUID-based script tracking
  - Four-schema architecture (utils/api/core/internal)
  - Three-tier role hierarchy (owner/admin/api)
  - Built-in HTTP framework
```

**Implementation:** `internal/cli/templates.go`

---

### version Command

**Purpose:** Display pgmi version information

**Signature:**
```bash
pgmi version
```

**Example:**
```bash
$ pgmi version
pgmi version 0.6.0
```

**Implementation:** `internal/cli/version.go`

---

### ai Command

**Purpose:** AI-digestible documentation for coding assistants

**Subcommands:**
```bash
pgmi ai                    # Overview (llms.txt style)
pgmi ai skills             # List all embedded skills
pgmi ai skill <name>       # Get full skill content
pgmi ai templates          # List template documentation
pgmi ai template <name>    # Get template-specific guide
```

**Philosophy:**
- Output goes to stdout (not stderr) so AI can capture it
- Markdown format - universally understood by AI assistants
- Skills preserve YAML frontmatter for metadata parsing

**Examples:**
```bash
# AI discovers pgmi capabilities
$ pgmi ai
# pgmi - AI Assistant Guide
# > PostgreSQL-native execution fabric...

# AI lists available skills
$ pgmi ai skills
# | Skill | Description |
# | `pgmi-sql` | Use when writing SQL/PL/pgSQL... |

# AI loads specific skill
$ pgmi ai skill pgmi-sql
# ---
# name: pgmi-sql
# description: "Use when writing SQL/PL/pgSQL or deploy.sql"
# ---
# ...full skill content...
```

**Implementation:** `internal/cli/ai.go`, `internal/ai/ai.go`

**Embedded Content:** Skills are embedded from `internal/ai/content/` at compile time. Source of truth is `.claude/skills/`.

---

## Flag Design Patterns

### Flag Types

**1. Boolean Flags (Switches)**
```go
var overwrite bool
deployCmd.Flags().BoolVar(&overwrite, "overwrite", false, "Drop and recreate database")

// Usage: --overwrite (no value needed)
```

**2. String Flags**
```go
var connection string
deployCmd.Flags().StringVarP(&connection, "connection", "c", "", "Connection string")

// Usage: --connection "..." or -c "..."
```

**3. StringArray Flags (Multiple Values)**
```go
var params []string
deployCmd.Flags().StringArrayVarP(&params, "param", "p", []string{}, "Parameters (key=value)")

// Usage: --param env=dev --param feature=enabled
```

**4. Duration Flags**
```go
var timeout time.Duration
deployCmd.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "Deployment timeout")

// Usage: --timeout 30m or --timeout 1h30m
```

### Required Flags

```go
// Mark flag as required
deployCmd.MarkFlagRequired("connection")

// Validation happens automatically
```

### Persistent Flags (Global)

```go
// Available to all subcommands
rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")

// Usage: pgmi deploy --verbose, pgmi metadata validate --verbose, etc.
// When enabled, also sets PostgreSQL session variable: SET client_min_messages = 'debug'
// This makes RAISE DEBUG messages from SQL scripts visible in output.
```

### Flag Validation

```go
// Custom validation
deployCmd.PreRunE = func(cmd *cobra.Command, args []string) error {
    if connection == "" && os.Getenv("PGMI_CONNECTION_STRING") == "" {
        return errors.New("connection string required (--connection or PGMI_CONNECTION_STRING)")
    }

    if database != "" && !overwrite {
        return errors.New("--database requires --overwrite flag")
    }

    return nil
}
```

---

## Parameter Handling

### Parameter Sources

**Priority (highest to lowest):**
1. CLI flags (`--param key=value`)
2. Params file (`--params-file params.json`)
3. Environment variables (future)

### Parameter Parsing

**CLI Parameters:**
```go
// Parse --param flags
func parseParams(paramFlags []string) (map[string]string, error) {
    params := make(map[string]string)

    for _, p := range paramFlags {
        parts := strings.SplitN(p, "=", 2)
        if len(parts) != 2 {
            return nil, fmt.Errorf("invalid parameter format: %s (expected key=value)", p)
        }

        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])

        if key == "" {
            return nil, fmt.Errorf("parameter key cannot be empty: %s", p)
        }

        params[key] = value
    }

    return params, nil
}
```

**Usage:**
```bash
# Single parameter
pgmi deploy . --param env=production

# Multiple parameters
pgmi deploy . \
  --param env=production \
  --param admin_password="SecurePass123!" \
  --param enable_audit=true
```

**Params File (`.env` format):**
```bash
# params.env
env=production
admin_password=SecurePass123!
enable_audit=true
```

```bash
pgmi deploy . --params-file params.env
```

**Security Note:**
- Parameters visible in process list (`ps aux`)
- Prefer params file for secrets (better, but still not ideal)
- Best: Use connection string for database credentials, avoid passing secrets as params

---

## Approver Interface

### Purpose

**Approver abstracts user interaction for destructive operations** (database overwrite confirmation).

### Interface Definition

**Location:** `pkg/pgmi/approver.go`

```go
// Approver handles user interaction for approval workflows,
// particularly for destructive operations like database overwriting.
type Approver interface {
    // RequestApproval prompts for confirmation before dropping and recreating a database.
    // Returns true if approved, false if denied.
    RequestApproval(ctx context.Context, dbName string) (bool, error)
}
```

### Implementations

**1. InteractiveApprover (Default)**
```go
// internal/ui/interactive_approver.go
type InteractiveApprover struct {
    reader io.Reader
    writer io.Writer
}

func (a *InteractiveApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
    // Prompts user to type database name for confirmation
    fmt.Fprintf(a.writer, "Database '%s' will be DROPPED and RECREATED.\n", dbName)
    fmt.Fprintf(a.writer, "Type the database name to confirm: ")
    // ... validation logic
}
```

**2. ForcedApprover (--force flag)**
```go
// internal/ui/forced_approver.go
type ForcedApprover struct{}

func (a *ForcedApprover) RequestApproval(ctx context.Context, dbName string) (bool, error) {
    // Shows countdown and automatically approves
    return true, nil
}
```

**Usage in CLI:**
```go
// internal/cli/deploy.go
var approver pgmi.Approver
if force {
    approver = &ForceApprover{}
} else {
    approver = &InteractiveApprover{reader: bufio.NewReader(os.Stdin)}
}

// Pass to deployer
deployer := services.NewDeployer(connector, approver)
```

**Benefits:**
- ✅ Testable (mock approver in tests)
- ✅ Flexible (different approval strategies)
- ✅ Separation of concerns (approval logic isolated)

---

## Interface-Based Decoupling

### Core Interfaces

pgmi defines interfaces in `pkg/pgmi/` to decouple CLI from implementation:

**1. Deployer Interface:** (`pkg/pgmi/deployer.go`)
```go
type Deployer interface {
    Deploy(ctx context.Context, config DeploymentConfig) error
}
```
- CLI calls deployer, doesn't know implementation details
- Easy to mock for testing
- Implementation can change without affecting CLI

**2. Connector Interface:** (`pkg/pgmi/connector.go`)
```go
type Connector interface {
    Connect(ctx context.Context) (*pgxpool.Pool, error)
}
```
- CLI creates connector via factory, passes to deployer
- Multiple connector implementations (standard, Azure Entra ID, future: AWS IAM, GCP)
- Returns pgx connection pool, not sql.DB

**3. Approver Interface:** (`pkg/pgmi/approver.go`)
```go
type Approver interface {
    RequestApproval(ctx context.Context, dbName string) (bool, error)
}
```
- Interactive vs forced mode
- Easy to test (mock approver)

**Benefits:**
- ✅ Separation of concerns (CLI vs business logic)
- ✅ Testability (mock interfaces)
- ✅ Extensibility (new implementations without changing CLI)
- ✅ Clean architecture (dependencies point inward)

---

## Error Handling

### Error Categories

**1. General Errors (Exit Code 1)**
- Unclassified errors

**2. Usage Errors (Exit Code 2)**
- Invalid flags or arguments
- Missing required arguments

**3. Panic (Exit Code 3)**
- Unexpected panics

**4. Config Errors (Exit Code 10)**
- Invalid configuration or parameters
- Invalid connection string

**5. Connection Errors (Exit Code 11)**
- Cannot connect to database
- Authentication failure
- Network issues

**6. Execution Errors (Exit Code 13)**
- SQL execution failures
- Transaction rollback
- Constraint violations

### Error Messages

**Good Error Messages:**
```
✅ "Connection string required. Use --connection flag or set PGMI_CONNECTION_STRING environment variable."
✅ "Failed to connect to postgresql://postgres@localhost:5432/mydb: connection refused. Is PostgreSQL running?"
✅ "Deployment failed at ./migrations/001.sql:15: syntax error near 'CRATE' (did you mean CREATE?)"
```

**Bad Error Messages:**
```
❌ "Error: connection failed"
❌ "Invalid input"
❌ "nil pointer dereference"
```

**Principles:**
- ✅ Be specific (what failed?)
- ✅ Provide context (where did it fail?)
- ✅ Suggest solutions (how to fix?)
- ✅ Show relevant details (connection string without password, file path, line number)

---

## Testing CLI Commands

### Unit Testing

**Test Flag Parsing:**
```go
// internal/cli/deploy_test.go
func TestParseParams(t *testing.T) {
    tests := []struct {
        name    string
        input   []string
        want    map[string]string
        wantErr bool
    }{
        {
            name:  "single parameter",
            input: []string{"env=production"},
            want:  map[string]string{"env": "production"},
        },
        {
            name:  "multiple parameters",
            input: []string{"env=prod", "feature=enabled"},
            want:  map[string]string{"env": "prod", "feature": "enabled"},
        },
        {
            name:    "invalid format",
            input:   []string{"invalid"},
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := parseParams(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("parseParams() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("parseParams() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Testing

**Test Full Command Execution:**
```go
func TestDeployCommand(t *testing.T) {
    // Create test database
    db := setupTestDatabase(t)
    defer db.Close()

    // Create temp directory with test files
    tmpDir := createTestProject(t)
    defer os.RemoveAll(tmpDir)

    // Execute deploy command
    cmd := exec.Command("pgmi", "deploy", tmpDir,
        "--connection", testConnectionString,
        "--database", "test_deploy",
        "--overwrite",
        "--force",
    )

    output, err := cmd.CombinedOutput()
    if err != nil {
        t.Fatalf("deploy failed: %v\nOutput: %s", err, output)
    }

    // Verify deployment succeeded
    verifyDatabaseSchema(t, db)
}
```

---

## Adding New Commands

### Checklist

When adding a new command:

1. [ ] Create command file in `internal/cli/`
2. [ ] Define command struct and flags
3. [ ] Implement command logic (or call service)
4. [ ] Add command to root in `root.go`
5. [ ] Write unit tests for flag parsing
6. [ ] Write integration tests for full execution
7. [ ] Update documentation (CLAUDE.md, README)
8. [ ] Consider: Does this belong in CLI or deploy.sql?

### Template

```go
// internal/cli/mycommand.go
package cli

import (
    "context"
    "fmt"

    "github.com/spf13/cobra"
)

var myCommandCmd = &cobra.Command{
    Use:   "mycommand [args]",
    Short: "Brief description",
    Long:  `Longer description with examples`,
    Args:  cobra.ExactArgs(1), // Or cobra.NoArgs, cobra.MinimumNArgs, etc.
    RunE:  runMyCommand,
}

var (
    // Command-specific flags
    myFlag string
)

func init() {
    // Add flags
    myCommandCmd.Flags().StringVar(&myFlag, "my-flag", "", "Description")

    // Mark required if needed
    myCommandCmd.MarkFlagRequired("my-flag")
}

func runMyCommand(cmd *cobra.Command, args []string) error {
    ctx := context.Background()

    // Validate flags
    if myFlag == "" {
        return fmt.Errorf("--my-flag is required")
    }

    // Implement command logic
    fmt.Printf("Running my command with flag: %s\n", myFlag)

    return nil
}
```

**Register in root.go:**
```go
func init() {
    rootCmd.AddCommand(myCommandCmd)
}
```

---

## Quick Reference

### Common Flag Patterns

| Pattern | Example | Use Case |
|---------|---------|----------|
| Required string | `--connection "..."` | Connection info |
| Optional string | `--database mydb` | Optional target |
| Boolean switch | `--overwrite` | Enable feature |
| Multiple values | `--param k1=v1 --param k2=v2` | Key-value pairs |
| Duration | `--timeout 30m` | Time limits |
| Short + long | `-c "..." / --connection "..."` | Convenience |

### Flag Validation

```go
// In PreRunE or RunE
if requiredFlag == "" {
    return errors.New("--required-flag is mandatory")
}

if value < 0 || value > 100 {
    return fmt.Errorf("--value must be between 0 and 100, got %d", value)
}

if !fileExists(path) {
    return fmt.Errorf("file not found: %s", path)
}
```

### Exit Codes

| Code | Meaning | Example |
|------|---------|---------|
| 0 | Success | Deployment completed |
| 1 | General error | Unclassified error |
| 2 | Usage error | Invalid flags, missing args |
| 3 | Panic | Internal unexpected crash |
| 10 | Config error | Invalid configuration or parameters |
| 11 | Connection error | Cannot reach database |
| 12 | Approval denied | User denied overwrite approval |
| 13 | Execution failed | SQL execution failed |
| 14 | deploy.sql missing | deploy.sql not found |

---

## See Also

- **pgmi-deployment skill:** Execution flow, plan-based model
- **pgmi-connections skill:** Connection factory, Connector interface
- **CLAUDE.md:** CLI design philosophy, timeout rationale
- **Cobra Documentation:** https://github.com/spf13/cobra

