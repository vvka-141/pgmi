# Script Metadata Guide

pgmi supports optional XML metadata blocks in SQL files that enable powerful deployment features: path-independent tracking, idempotency control, and explicit execution ordering.

---

## Metadata is Optional

**You don't need metadata to use pgmi.**

The basic template works perfectly without any metadata. Files execute in lexicographic path order, and pgmi generates deterministic fallback identifiers from file paths.

```
myproject/
├── deploy.sql           # No metadata needed
├── migrations/
│   ├── 001_users.sql    # No metadata needed
│   └── 002_orders.sql   # No metadata needed
└── __test__/
    └── test_users.sql   # No metadata needed
```

This is often sufficient. Add metadata only when you need its specific capabilities.

---

## When to Use Metadata

### Use metadata when you need:

| Capability | Without Metadata | With Metadata |
|------------|------------------|---------------|
| **Script identity** | Path-based (changes if file moves) | UUID-based (survives renames) |
| **Execution order** | Lexicographic by path | Explicit via sort keys |
| **Re-execution control** | All scripts re-run every deploy | Idempotent vs one-time |
| **Multi-phase execution** | Not possible | Same file at multiple stages |

### Consider metadata for:

- **Production deployments** where you need idempotency control (one-time migrations vs always-update functions)
- **Large projects** (20+ SQL files) where explicit ordering matters
- **Team projects** where file paths may change during refactoring
- **Complex deployments** with distinct phases (bootstrap → migrate → setup → seed)

### Skip metadata for:

- Simple projects with < 10 SQL files
- Linear migrations (001_, 002_, 003_ naming works fine)
- Prototype or throwaway projects
- Test files (`__test__/` directories - metadata is ignored there anyway)

---

## Metadata Syntax

Metadata lives in a SQL block comment at the top of the file:

```sql
/*
<pgmi-meta
    id="550e8400-e29b-41d4-a716-446655440000"
    idempotent="true">
  <description>What this script does</description>
  <sortKeys>
    <key>10-utils/0010</key>
  </sortKeys>
</pgmi-meta>
*/

-- Your SQL code follows...
CREATE OR REPLACE FUNCTION ...
```

### Attributes

| Attribute | Required | Type | Description |
|-----------|----------|------|-------------|
| `id` | Yes | UUID | Unique identifier (RFC 4122 format) |
| `idempotent` | Yes | boolean | `true` = always re-run, `false` = run once |

### Child Elements

| Element | Required | Description |
|---------|----------|-------------|
| `<description>` | No | Human-readable explanation |
| `<sortKeys>` | No | Execution order keys (defaults to file path) |

---

## Script Identity (UUID)

The `id` attribute gives your script a stable identity independent of its file path.

### Why This Matters

```
# Before refactoring:
migrations/001_create_users.sql  → tracked by path

# After refactoring:
database/schema/users.sql         → NEW path = lost tracking history!
```

With metadata UUID:
```sql
/*
<pgmi-meta id="a1b2c3d4-e5f6-7890-abcd-ef1234567890" idempotent="false">
  <description>Create users table</description>
</pgmi-meta>
*/
```

Now the file can move anywhere and pgmi still knows it's the same script. One-time migrations won't re-run just because you reorganized your project.

### Generating UUIDs

```bash
# Linux/Mac
uuidgen

# PowerShell
[guid]::NewGuid()

# PostgreSQL
SELECT gen_random_uuid();

# Or use pgmi's scaffold command (see CLI Commands below)
```

### Fallback Identity

Files without metadata get a deterministic UUID based on their path:
```
UUID v5 = SHA1(namespace, normalized_path)
```

This means:
- `./migrations/001.sql` always gets the same fallback UUID
- Case changes (`./SETUP/File.SQL` → `./setup/file.sql`) get the same UUID
- But path changes (`./old/file.sql` → `./new/file.sql`) get different UUIDs

Use explicit metadata when path stability matters.

---

## Idempotency Control

The `idempotent` attribute controls whether a script runs every deployment or just once.

### `idempotent="true"` - Always Re-run

Use for scripts that are safe (or intended) to run multiple times:

```sql
/*
<pgmi-meta id="..." idempotent="true">
  <description>User API handlers - always update to latest</description>
</pgmi-meta>
*/

-- Safe to run repeatedly
CREATE OR REPLACE FUNCTION api.get_users()
RETURNS SETOF users AS $$
    SELECT * FROM users WHERE deleted_at IS NULL;
$$ LANGUAGE sql;
```

**Use for:**
- `CREATE OR REPLACE FUNCTION/VIEW`
- `CREATE TABLE IF NOT EXISTS`
- Configuration updates
- Reference data upserts (`ON CONFLICT DO UPDATE`)

### `idempotent="false"` - Run Once

Use for scripts that should only execute once:

```sql
/*
<pgmi-meta id="..." idempotent="false">
  <description>Add email column (one-time migration)</description>
</pgmi-meta>
*/

-- Should only run once
ALTER TABLE users ADD COLUMN email TEXT;
```

**Use for:**
- `ALTER TABLE ADD COLUMN`
- Data migrations
- Destructive operations (`DROP`, `TRUNCATE`)
- Non-idempotent `INSERT` statements

### How Tracking Works

The advanced template maintains an execution log:
```sql
internal.deployment_script_execution_log
├── script_id (UUID)
├── path
├── idempotent
├── checksum
└── executed_at
```

On each deployment:
- **Idempotent scripts**: Always execute, log updated
- **Non-idempotent scripts**: Skip if already logged, show `[SKIP]` notice

---

## Execution Order (Sort Keys)

Sort keys give you explicit control over execution order, overriding the default lexicographic path ordering.

### Basic Usage

```sql
/*
<pgmi-meta id="..." idempotent="true">
  <sortKeys>
    <key>10-utils/0010</key>
  </sortKeys>
</pgmi-meta>
*/
```

Scripts execute in sort key order (ascending), with path as tiebreaker.

### Sort Key Conventions

Choose a convention and stick to it:

**Layer-based (recommended for frameworks):**
```
000             # Bootstrap (roles, schemas, extensions)
001/000         # Layer 1: Utilities
002/000         # Layer 2: Internal infrastructure
003/000         # Layer 3: Core domain
004/000         # Layer 4: API layer
005/000         # Layer 5: Application code
```

**Phase-based (recommended for migrations):**
```
01-pre-deployment/001
02-migrations/001
03-setup/001
04-post-deployment/001
```

**Date-based (for chronological migrations):**
```
migrations/2025-01-15/001
migrations/2025-01-15/002
migrations/2025-01-16/001
```

### No Sort Keys = Path Order

If you omit `<sortKeys>`, the file path becomes the sort key:

```sql
/*
<pgmi-meta id="..." idempotent="true">
  <description>Uses path as sort key</description>
</pgmi-meta>
*/
```

This file sorts by its path (e.g., `./migrations/001_users.sql`).

---

## Multi-Phase Execution

A single script can execute at multiple deployment stages by specifying multiple sort keys.

### Example: Bootstrap + Seed

```sql
/*
<pgmi-meta id="..." idempotent="true">
  <description>Create roles schema, then populate later</description>
  <sortKeys>
    <key>000/020</key>     <!-- Phase 1: Create schema -->
    <key>005/010</key>     <!-- Phase 2: Seed data -->
  </sortKeys>
</pgmi-meta>
*/

-- Runs in Phase 1 (bootstrap)
CREATE SCHEMA IF NOT EXISTS roles;

-- Runs in Phase 2 (seeding)
INSERT INTO roles.role (name) VALUES ('admin'), ('user')
ON CONFLICT (name) DO NOTHING;
```

### How It Works

Each `<key>` generates a separate execution entry:

```
Execution Plan:
#5:  sort_key='000/020', path='./roles.sql'  (first execution)
#47: sort_key='005/010', path='./roles.sql'  (second execution)
```

The same file content executes twice at different stages.

### Use Cases

- **Deferred initialization**: Create schema early, populate data later
- **Circular dependencies**: Define types early, create functions that use them later
- **Configuration refresh**: Apply initial config, then override after other setup

---

## CLI Commands

pgmi provides commands to work with metadata:

### Scaffold Metadata

Generate metadata blocks for files that don't have them:

```bash
# Preview what would be generated
pgmi metadata scaffold ./myproject

# Write metadata to files
pgmi metadata scaffold ./myproject --write

# Generate with idempotent=false default
pgmi metadata scaffold ./myproject --idempotent=false --write
```

### Validate Metadata

Check for syntax errors and conflicts:

```bash
# Human-readable output
pgmi metadata validate ./myproject

# JSON output for CI/CD
pgmi metadata validate ./myproject --json
```

**Validates:**
- XML syntax
- Required attributes (`id`, `idempotent`)
- UUID format
- Duplicate IDs across files
- Empty sort keys

### Preview Execution Plan

See the execution order before deploying:

```bash
# Human-readable table
pgmi metadata plan ./myproject

# JSON output
pgmi metadata plan ./myproject --json
```

---

## Examples

### Example 1: Simple Utility (Idempotent)

```sql
/*
<pgmi-meta
    id="a1b2c3d4-e5f6-4789-a012-3456789abcdef"
    idempotent="true">
  <description>Safe UUID casting utility</description>
  <sortKeys>
    <key>001/010</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION utils.try_cast_uuid(input TEXT)
RETURNS UUID AS $$
BEGIN
    RETURN input::UUID;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN NULL;
END;
$$ LANGUAGE plpgsql IMMUTABLE;
```

**Result**: Executes every deployment, always updates function definition.

### Example 2: One-Time Migration

```sql
/*
<pgmi-meta
    id="f9e8d7c6-b5a4-4321-9876-543210fedcba"
    idempotent="false">
  <description>Add soft delete support</description>
  <sortKeys>
    <key>02-migrations/2025-01-15/001</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;
CREATE INDEX idx_users_active ON users(id) WHERE deleted_at IS NULL;
```

**Result**: Executes once, skipped on subsequent deployments.

### Example 3: Minimal Metadata

```sql
/*
<pgmi-meta id="12345678-1234-5678-1234-567812345678" idempotent="true">
</pgmi-meta>
*/

-- Uses path for sort order, tracks by UUID
CREATE TABLE IF NOT EXISTS logs (id SERIAL PRIMARY KEY);
```

**Result**: Minimal metadata for UUID tracking, path-based ordering.

---

## Error Messages

pgmi provides actionable error messages for metadata issues:

```
metadata error in ./migrations/001_users.sql (line 5): id attribute is required

Hint: Each script must have a unique identifier.
  Generate with: uuidgen (Linux/Mac), [guid]::NewGuid() (PowerShell)
```

```
metadata error: duplicate script ID found

ID: 550e8400-e29b-41d4-a716-446655440000
Files:
  - ./migrations/001_users.sql
  - ./setup/users_setup.sql

Hint: Each script must have a globally unique identifier.
  Generate a new UUID for one of these files.
```

---

## Migration from Basic to Advanced

If you start with the basic template and later need metadata features:

1. **Assess need**: Do you actually need idempotency control or path-independent tracking?

2. **Scaffold metadata**:
   ```bash
   pgmi metadata scaffold ./myproject --write
   ```

3. **Review and customize**:
   - Adjust `idempotent` flags (migrations → false, functions → true)
   - Set meaningful sort keys if order matters
   - Add descriptions for documentation

4. **Validate**:
   ```bash
   pgmi metadata validate ./myproject
   ```

5. **Preview execution**:
   ```bash
   pgmi metadata plan ./myproject
   ```

---

## Summary

| Feature | When to Use | How |
|---------|-------------|-----|
| **UUID identity** | Files may be renamed/moved | Add `id="..."` attribute |
| **Idempotency** | Mix of one-time and repeatable scripts | Set `idempotent="true/false"` |
| **Sort keys** | Need explicit ordering | Add `<sortKeys><key>...</key></sortKeys>` |
| **Multi-phase** | Same script at different stages | Multiple `<key>` elements |

**Remember**: Metadata is a power tool, not a requirement. Start simple, add metadata when you need its specific capabilities.
