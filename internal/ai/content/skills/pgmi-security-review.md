---
name: pgmi-security-review
description: "Use when reviewing for SQL injection, RLS, or security"
user_invocable: true
---


**Purpose**: Cross-cutting security expertise for all pgmi code (SQL, HTTP, Go). Ensures secure patterns for injection prevention, secret handling, authentication, authorization, and input validation.

**Used By**:
- postgres-sql-reviewer (SQL injection, RLS, encryption)
- http-expert-reviewer (API security, authentication)
- golang-expert-reviewer (connection security, secrets)
- general-purpose (any security-sensitive code)
- change-planner (security-critical changes)

**Auto-Load With**:
- `pgmi-sql` skill (SQL injection prevention)
- Keywords: "security", "auth", "encrypt", "secret", "password", "token", "RLS"

**Load For**: Preventing security vulnerabilities, secure coding patterns, compliance

---

## Threat Model: Deployment-Time vs Runtime

**CRITICAL**: pgmi has two distinct execution contexts with fundamentally different threat models. Confusing them produces false positives.

### Deployment-Time Code (Trusted Context)

Code executed by pgmi during deployment runs in a **privileged, trusted context**:
- Executed by an operator/CI pipeline with database admin credentials
- Not exposed to external input — all values come from project files and CLI parameters
- Runs once during deployment, not on every request

**Examples**: DDL statements, handler/route registrations, role grants, schema setup, extension installation, seed data.

**What NOT to flag in deployment-time code**:
- "Handler registration could be tampered with" — anyone who can modify registrations already has full database access
- "Route table INSERT could be exploited" — these run during deployment, not at runtime
- "Missing input validation on DDL parameters" — parameters come from developer-controlled files
- "Privilege escalation via role grants" — the deployer already operates as superuser/admin

**Rule**: If the code only runs during `pgmi deploy`, the threat actor would need deployment access. At that point, they can already DROP DATABASE. Flagging deployment-time code with runtime threat scenarios is a false positive.

### Specific Non-Issues (Common False Alarms)

**URI/Regex patterns in handler registration**: Handler registration functions like `api.create_or_replace_rest_handler()` accept regex patterns for URI matching. While pathological regex could cause performance issues, this is NOT a security vulnerability because:
- Registration happens during deployment, not at runtime
- Patterns come from developer-written SQL files, not user input
- "ReDoS attack" would require compromising source code or CI/CD pipeline
- At that point, attacker could inject any malicious SQL

**Verdict**: Code quality concern (code review for regex efficiency), not security finding.

**Handler body construction (`p_handler_body`)**: Handler registration functions dynamically construct PostgreSQL functions using `format()` with a UUID-based dollar-quote boundary:

```sql
v_boundary := 'hb_' || replace(v_id::text, '-', '');
v_function_sql := format($sql$CREATE OR REPLACE FUNCTION %I.%I(request api.rest_request)
RETURNS api.http_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$, schema, name, v_boundary, p_handler_body, v_boundary);
```

This is **NOT SQL injection** because:
- `p_handler_body` is DEVELOPER CODE from source files (e.g., `api/examples.sql`)
- It has the SAME TRUST LEVEL as any other SQL in `deploy.sql`
- Registration functions are NEVER called at runtime by user requests
- Gateways (`api.rest_invoke`) only execute pre-registered handlers
- `api_role` (runtime role) has only SELECT on handler tables, cannot INSERT/UPDATE
- To exploit boundary escape, attacker would need repository write access
- With repository access, attacker can write ANY SQL directly without boundary tricks

**This is analogous to:**
- `EXECUTE v_file.content` in deploy.sql (executes developer-written SQL files)
- Every other `EXECUTE` statement in the deployment process

**Verdict**: Same trust model as all deployment code. NOT a security finding.

### Runtime Code (Untrusted Context)

Code executed at runtime (API handlers, user-facing functions) handles **untrusted external input**:
- Called by end users via HTTP gateway
- Input values are attacker-controlled
- Runs continuously in production

**Examples**: API handler functions, authentication logic, data query functions, business logic.

**What to flag in runtime code**: SQL injection, missing authorization, privilege escalation, data leaks — all standard runtime security concerns apply.

### Decision Flowchart

```
Is this code executed during pgmi deploy?
├── YES → Deployment-time (trusted context)
│   └── Flag ONLY: secrets in logs, permanent credential storage
│       Do NOT flag: tampering, injection, privilege escalation
└── NO → Runtime (untrusted context)
    └── Full security review applies
```

---

## Security Boundary: Gateway vs PostgreSQL

**CRITICAL**: pgmi HTTP handlers run behind a web server/API gateway. This affects what security concerns to review at runtime.

### Security Responsibilities

| Security Concern | Gateway | PostgreSQL |
|-----------------|---------|------------|
| TLS/HTTPS | ✅ | ❌ |
| DoS protection (rate limiting) | ✅ | ❌ |
| Request size limits | ✅ | ❌ |
| Header validation | ✅ | ❌ |
| JWT/OAuth token validation | ✅ | ❌ |
| Error message sanitization | ✅ | ❌ |
| SQL injection prevention | ❌ | ✅ |
| Row-Level Security (RLS) | ❌ | ✅ |
| Data encryption | ❌ | ✅ |
| Audit logging | ❌ | ✅ |
| Secret storage | ❌ | ✅ |
| Constraint validation | ❌ | ✅ |

### What to Flag (PostgreSQL Security)

- SQL injection vulnerabilities
- Missing RLS policies on multi-tenant tables
- Over-privileged grants
- Secrets in logs or error messages
- Missing constraint validation
- SECURITY DEFINER functions without proper checks

### What NOT to Flag (Gateway Security)

- SQLERRM exposure in error responses (gateway sanitizes)
- Missing CORS headers (gateway adds)
- Missing rate limiting (gateway handles)
- Header size validation (gateway validates)
- DoS protection (gateway enforces)

### Auth Token in Transaction-Local Session Config (Non-Issue)

The pattern of storing auth tokens via `set_config('auth.token', ..., true)` is acceptable:

```sql
-- This is ACCEPTABLE in DMZ architecture
PERFORM set_config('auth.token', p_headers->'authorization', true);
```

**Why this is NOT a vulnerability:**
- `true` parameter makes it transaction-local (not session-wide)
- Other concurrent sessions cannot see transaction-local settings
- Automatically cleared when transaction ends
- Database sits behind API gateway (DMZ) - direct access requires compromising API first
- Handler code legitimately needs token access (JWT claims, service forwarding)
- If attacker has SQL execution in the transaction, they already have full database access

**The security boundary is the API gateway, not PostgreSQL session state.**

**Verdict**: Defense-in-depth consideration, not a practical vulnerability. Do NOT flag as security issue.

---

## Security Review Principles

### Defense in Depth
Multiple layers of security controls, so failure of one doesn't compromise the system.

**Example Layers**:
1. **Input Validation**: Reject malformed data at entry
2. **Parameterized Queries**: Prevent SQL injection
3. **Principle of Least Privilege**: Minimal grants
4. **Row-Level Security**: Additional table-level filtering
5. **Audit Logging**: Detect suspicious activity

### Fail Securely
When errors occur, default to denying access, not granting it.

```sql
-- ✅ GOOD: Default deny
CREATE FUNCTION api.authenticate(p_token TEXT)
RETURNS UUID AS $$
DECLARE
    v_user_id UUID := NULL;
BEGIN
    SELECT user_id INTO v_user_id
    FROM auth_tokens
    WHERE token = p_token AND expires_at > now();

    IF v_user_id IS NULL THEN
        RAISE EXCEPTION 'Invalid or expired token' USING ERRCODE = 'P0001';
    END IF;

    RETURN v_user_id;
END;
$$ LANGUAGE plpgsql;

-- ❌ BAD: Default allow
CREATE FUNCTION api.authenticate(p_token TEXT)
RETURNS UUID AS $$
BEGIN
    RETURN (SELECT user_id FROM auth_tokens WHERE token = p_token LIMIT 1);
    -- Returns NULL on failure (caller might not check!)
END;
$$ LANGUAGE plpgsql;
```

### Least Privilege
Grant minimum permissions necessary for each role/user.

```sql
-- ✅ GOOD: Granular grants
GRANT SELECT ON public.users TO app_read_role;
GRANT SELECT, INSERT, UPDATE ON public.users TO app_write_role;
GRANT ALL ON public.users TO admin_role;

-- ❌ BAD: Over-privileged
GRANT ALL ON ALL TABLES IN SCHEMA public TO app_read_role;
```

---

## SQL Injection Prevention

### PostgreSQL (format %I/%L)

**Critical Rule**: NEVER concatenate user input into SQL strings.

```sql
-- ❌ CRITICAL: SQL injection vulnerability
CREATE FUNCTION unsafe_query(p_table_name TEXT, p_user_input TEXT)
RETURNS SETOF RECORD AS $$
BEGIN
    RETURN QUERY EXECUTE 'SELECT * FROM ' || p_table_name || ' WHERE name = ''' || p_user_input || '''';
    -- Attacker input: "'; DROP TABLE "user"; --"
END;
$$ LANGUAGE plpgsql;

-- ✅ SAFE: format() with %I for identifiers, %L for literals
CREATE FUNCTION safe_query(p_table_name TEXT, p_user_input TEXT)
RETURNS SETOF RECORD AS $$
BEGIN
    RETURN QUERY EXECUTE format('SELECT * FROM %I WHERE name = %L', p_table_name, p_user_input);
    -- %I quotes identifier (table name), %L quotes literal (user input)
END;
$$ LANGUAGE plpgsql;
```

**Format Specifiers**:
- `%I`: Identifier (table, column, schema names) - quoted with double quotes
- `%L`: Literal (strings, numbers) - quoted with single quotes, escaped
- `%s`: Unquoted string (dangerous, avoid for user input)

**Review Checklist**:
- [ ] No string concatenation (`||`) for SQL construction with user input?
- [ ] `format(%I)` used for identifiers (tables, columns)?
- [ ] `format(%L)` used for literals (user-provided values)?
- [ ] Dynamic SQL reviewed for injection vectors?

### Go (Parameterized Queries)

```go
// ❌ CRITICAL: SQL injection vulnerability
func getUserByName(ctx context.Context, db *pgx.Conn, name string) (*User, error) {
    query := fmt.Sprintf(`SELECT id, name, email FROM "user" WHERE name = '%s'`, name)
    // Attacker input: "admin' OR '1'='1"
    row := db.QueryRow(ctx, query)
    // ...
}

// ✅ SAFE: Parameterized query
func getUserByName(ctx context.Context, db *pgx.Conn, name string) (*User, error) {
    query := `SELECT id, name, email FROM "user" WHERE name = $1`
    row := db.QueryRow(ctx, query, name)
    // PostgreSQL driver safely escapes parameters
    // ...
}
```

**Review Checklist**:
- [ ] No `fmt.Sprintf()` or string concatenation for SQL queries?
- [ ] Parameterized queries ($1, $2, ...) for all user input?
- [ ] Dynamic SQL reviewed for injection vectors?

---

## Secret Handling

### Never Log Secrets

```go
// ❌ BAD: Secret in log output
func connect(connStr string) {
    log.Printf("Connecting with: %s", connStr) // Leaks password!
    // ...
}

// ✅ GOOD: Redact secrets
func connect(connStr string) {
    log.Printf("Connecting to database")
    // Or: log parsed connection without password
    // ...
}
```

```sql
-- ❌ BAD: Secret in log
DO $$
DECLARE
    v_api_key TEXT := COALESCE(current_setting('pgmi.api_key', true), 'default');
BEGIN
    RAISE NOTICE 'Using API key: %', v_api_key; -- Leaks secret!
END $$;

-- ✅ GOOD: Log without secret value
DO $$
BEGIN
    RAISE NOTICE 'API key configured: %', (COALESCE(current_setting('pgmi.api_key', true), '') != '');
END $$;
```

### Session-Scoped Storage

**pgmi Pattern**: Secrets passed via `--param` are stored as session-scoped configuration variables.

```sql
-- ✅ GOOD: Secrets passed via --param, stored in session-scoped config
-- pgmi deploy . --param api_key=secret_value
-- Auto-cleanup on session end

-- Access only when needed
SELECT current_setting('pgmi.api_key', true);

-- ❌ BAD: Secrets in permanent tables
INSERT INTO config (key, value) VALUES ('api_key', 'secret_value');
-- Persists across sessions, visible to all users with SELECT!
```

**Review Checklist**:
- [ ] Secrets NOT logged or printed?
- [ ] Secrets NOT stored in permanent tables?
- [ ] Secrets passed via `--param` CLI, not hardcoded?
- [ ] Secret values NOT included in error messages?

### Connection String Security

```go
// ❌ BAD: Password in code
connStr := "postgresql://user:hardcoded_password@localhost/db"

// ✅ GOOD: Password from environment or user input
password := os.Getenv("PGPASSWORD")
connStr := fmt.Sprintf("postgresql://user:%s@localhost/db", password)

// ✅ BETTER: Use connection config struct
config, err := pgx.ParseConfig(os.Getenv("DATABASE_URL"))
```

```bash
# ✅ GOOD: Environment variable
export DATABASE_URL="postgresql://user:pass@localhost/db"
pgmi deploy ./migrations

# ✅ GOOD: pgpass file (~/.pgpass)
# Format: hostname:port:database:username:password
localhost:5432:mydb:myuser:mypass

# ❌ BAD: Password in command line (visible in process list!)
pgmi deploy ./migrations --connection "postgresql://user:pass@localhost/db"
```

---

## Authentication and Authorization

### Authentication (Who are you?)

**Pattern**: Validate credentials, establish identity.

```sql
-- ✅ GOOD: Token-based authentication
CREATE FUNCTION api.authenticate(p_token TEXT)
RETURNS UUID AS $$
DECLARE
    v_user_id UUID;
BEGIN
    SELECT user_id INTO v_user_id
    FROM auth_tokens
    WHERE token = p_token AND expires_at > now();

    IF v_user_id IS NULL THEN
        RAISE EXCEPTION 'Unauthorized' USING ERRCODE = 'P0001';
    END IF;

    -- Set session variable for downstream use
    PERFORM set_config('app.current_user_id', v_user_id::TEXT, false);

    RETURN v_user_id;
END;
$$ LANGUAGE plpgsql STABLE;
```

**Review Checklist**:
- [ ] Authentication required for protected endpoints?
- [ ] Token expiration enforced?
- [ ] Failed authentication returns 401 Unauthorized?
- [ ] Session established after successful auth?

### Authorization (What can you do?)

**Pattern 1: Function-Level Checks**
```sql
CREATE FUNCTION api.delete_user(p_token TEXT, p_user_id UUID)
RETURNS JSON AS $$
DECLARE
    v_caller_id UUID;
BEGIN
    -- Authenticate
    v_caller_id := api.authenticate(p_token);

    -- Authorize: Only admins or self can delete
    IF NOT (
        EXISTS (SELECT 1 FROM "user" WHERE id = v_caller_id AND role = 'admin')
        OR v_caller_id = p_user_id
    ) THEN
        RETURN json_build_object('status', 403, 'body', json_build_object('error', 'Forbidden'));
    END IF;

    -- Delete user
    DELETE FROM "user" WHERE id = p_user_id;
    RETURN json_build_object('status', 204);
END;
$$ LANGUAGE plpgsql;
```

**Pattern 2: Row-Level Security (RLS)**
```sql
-- Enable RLS
ALTER TABLE "user" ENABLE ROW LEVEL SECURITY;

-- Policy: Users can only update their own row
CREATE POLICY user_update_own ON users
    FOR UPDATE
    TO app_user_role
    USING (id = current_setting('app.current_user_id')::UUID);

-- Policy: Admins can update any row
CREATE POLICY admin_update_all ON users
    FOR UPDATE
    TO admin_role
    USING (true);
```

**Pattern 3: SECURITY DEFINER and the FORCE-RLS trap (multi-tenant)**

The single most common multi-tenant hole in a pgmi advanced-template app: **RLS does not constrain the table owner unless `FORCE ROW LEVEL SECURITY` is set, and every SECURITY DEFINER function runs as the owner.** Domain kernels are SECURITY DEFINER. So on a table with `ENABLE` (but not `FORCE`) RLS, a kernel `UPDATE core.invoice SET ... WHERE id = p_id` runs with RLS *out of the loop* — it can read and write any tenant's rows. The policy looks protective in `\d` output and in customer-role tests, yet the actual write path bypasses it.

Two correct postures — pick one per table and apply it consistently:

```sql
-- Posture A (preferred): FORCE RLS so the owner is constrained too.
-- core.apply_org_rls() does ENABLE + FORCE + the org-scoped policy set, so a
-- DEFINER kernel that forgets an explicit predicate is STILL tenant-scoped.
SELECT core.apply_org_rls('core.invoice');

-- Posture B: ENABLE-only RLS, and EVERY DEFINER mutation carries the predicate.
UPDATE core.invoice SET status = 'paid'
WHERE id = p_id
  AND organization_id = ANY (api.current_member_org_ids());  -- mandatory
```

**What to flag:**
- [ ] A SECURITY DEFINER function doing `INSERT`/`UPDATE`/`DELETE`/`SELECT` on an org-scoped table **without** an `organization_id` (tenant) predicate, where the table is not `FORCE`-RLS protected. This is a cross-tenant read/write, not a style nit.
- [ ] An org-scoped table with `ENABLE` but not `FORCE` RLS whose writes go through DEFINER kernels — either switch to `core.apply_org_rls` (FORCE) or require the predicate in every kernel.
- [ ] Existence probes in auth/identity handlers (`EXISTS(SELECT 1 FROM ... WHERE email = ...)`) without a tenant predicate — they enable cross-tenant enumeration.

**How to audit:** grep every `SECURITY DEFINER` function body for DML and confirm a tenant predicate (or FORCE RLS on the target). Add a per-domain test that sets a member context for tenant A and asserts tenant B's ids are unreachable **through the kernel**, not only through a view.

**Gateway trust boundary:** RLS and `api.current_*` derive identity entirely from session GUCs (`auth.idp_subject`) that the gateway sets. The gateway is therefore part of the auth system: it MUST set those GUCs only from a verified token and **strip any client-supplied identity/tenant headers** (`x-user-id`, `x-user-email`, `x-tenant-id`, `authorization`) before forwarding. Forwarding client headers wholesale lets a caller forge identity and select another tenant — flag it as a critical finding.

**Review Checklist**:
- [ ] Authorization checks after authentication?
- [ ] Failed authorization returns 403 Forbidden?
- [ ] Principle of least privilege enforced?
- [ ] RLS enabled for multi-tenant data — and `FORCE`d, or every DEFINER mutation carries the tenant predicate?
- [ ] Gateway strips client-supplied identity/tenant headers and sets auth GUCs only from a verified token?

---

## Input Validation

### Validate Early, Fail Fast

```sql
-- ✅ GOOD: Validation before business logic
CREATE FUNCTION api.create_user(p_email TEXT, p_name TEXT)
RETURNS JSON AS $$
BEGIN
    -- Validate email format
    IF p_email IS NULL OR p_email !~ '^[^@]+@[^@]+\.[^@]+$' THEN
        RETURN json_build_object('status', 400, 'body', json_build_object('error', 'Invalid email format'));
    END IF;

    -- Validate name length
    IF p_name IS NULL OR LENGTH(TRIM(p_name)) < 2 THEN
        RETURN json_build_object('status', 400, 'body', json_build_object('error', 'Name must be at least 2 characters'));
    END IF;

    -- Business logic
    INSERT INTO "user" (email, name) VALUES (p_email, p_name);
    RETURN json_build_object('status', 201, 'body', json_build_object('success', true));
END;
$$ LANGUAGE plpgsql;
```

### Type Safety

```go
// ✅ GOOD: Strong typing
type CreateUserRequest struct {
    Email string `json:"email"`
    Name  string `json:"name"`
}

func createUser(ctx context.Context, db *pgx.Conn, req *CreateUserRequest) error {
    // Type safety from struct
    if req.Email == "" {
        return errors.New("email required")
    }
    // ...
}

// ❌ BAD: map[string]interface{} (weak typing)
func createUser(ctx context.Context, db *pgx.Conn, data map[string]interface{}) error {
    email, ok := data["email"].(string) // Runtime type assertion (fragile)
    if !ok {
        return errors.New("invalid email type")
    }
    // ...
}
```

### Range Checking

```sql
-- ✅ GOOD: Range validation
CREATE FUNCTION api.set_age(p_user_id UUID, p_age INT)
RETURNS JSON AS $$
BEGIN
    IF p_age < 0 OR p_age > 150 THEN
        RETURN json_build_object('status', 400, 'body', json_build_object('error', 'Age must be between 0 and 150'));
    END IF;

    UPDATE "user" SET age = p_age WHERE id = p_user_id;
    RETURN json_build_object('status', 200);
END;
$$ LANGUAGE plpgsql;
```

### Path Traversal Prevention

```go
// ❌ BAD: Path traversal vulnerability
func loadFile(userPath string) ([]byte, error) {
    return os.ReadFile(userPath) // Attacker: "../../etc/passwd"
}

// ✅ GOOD: Validate path is within allowed directory
func loadFile(userPath string, baseDir string) ([]byte, error) {
    fullPath := filepath.Join(baseDir, filepath.Clean(userPath))

    // Ensure resolved path is still within baseDir
    if !strings.HasPrefix(fullPath, baseDir) {
        return nil, errors.New("invalid path: outside base directory")
    }

    return os.ReadFile(fullPath)
}
```

**Review Checklist**:
- [ ] Input validation before business logic?
- [ ] Type safety enforced (strong types, not `interface{}`)?
- [ ] Range checks for numeric inputs?
- [ ] Path traversal prevention for file operations?
- [ ] Regex validation for structured strings (email, URL, UUID)?

---

## Connection Security

### SSL/TLS

**PostgreSQL Connection**:
```go
// ❌ BAD: Plaintext connection (sslmode=disable)
connStr := "postgresql://user:pass@localhost/db?sslmode=disable"

// ✅ GOOD: Require SSL
connStr := "postgresql://user:pass@localhost/db?sslmode=require"

// ✅ BETTER: Verify CA (prevent MITM)
connStr := "postgresql://user:pass@localhost/db?sslmode=verify-ca&sslrootcert=/path/to/ca.crt"

// ✅ BEST: Verify full identity
connStr := "postgresql://user:pass@localhost/db?sslmode=verify-full&sslrootcert=/path/to/ca.crt"
```

**SSL Mode Options**:
- `disable`: No SSL (plaintext)
- `allow`: Try SSL, fallback to plaintext
- `prefer`: Try SSL, fallback to plaintext (default)
- `require`: Require SSL (but don't verify cert)
- `verify-ca`: Require SSL, verify CA
- `verify-full`: Require SSL, verify CA + hostname

**Review Checklist**:
- [ ] `sslmode=require` or stronger for production?
- [ ] Certificate validation enabled (`verify-ca`/`verify-full`)?
- [ ] Certificates from trusted CA?

### Certificate-Based Authentication (mTLS)

pgmi provides first-class CLI support for TLS client certificates via `--sslcert`, `--sslkey`, `--sslrootcert` flags, `PGSSLCERT`/`PGSSLKEY`/`PGSSLROOTCERT`/`PGSSLPASSWORD` env vars, and `pgmi.yaml` (`connection.sslcert`/`sslkey`/`sslrootcert`). Cert flags are additive — they work alongside `--connection` or granular flags without conflict.

```bash
# Via CLI flags
pgmi deploy . -d myapp \
  --sslmode verify-full \
  --sslcert /path/to/client.crt \
  --sslkey /path/to/client.key \
  --sslrootcert /path/to/ca.crt

# Via connection string
connStr := "postgresql://user@localhost/db?sslmode=verify-full&sslcert=/path/to/client.crt&sslkey=/path/to/client.key&sslrootcert=/path/to/ca.crt"
```

**PostgreSQL Server Config**:
```
# pg_hba.conf
hostssl all all 0.0.0.0/0 cert clientcert=verify-full
```

**Review Checklist**:
- [ ] `SSLPassword` NOT passed via CLI flag or pgmi.yaml (env var only)?
- [ ] Certificate file paths not logged or exposed in error messages?
- [ ] `sslmode=verify-full` used with client certificates for full MITM protection?

---

## Encryption

### At Rest (Column-Level Encryption)

```sql
-- Install pgcrypto extension
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ✅ Encrypt sensitive data before storage
INSERT INTO "user" (id, email, encrypted_ssn)
VALUES (
    gen_random_uuid(),
    'alice@example.com',
    pgp_sym_encrypt('123-45-6789', current_setting('app.encryption_key'))
);

-- ✅ Decrypt on retrieval
SELECT
    id,
    email,
    pgp_sym_decrypt(encrypted_ssn, current_setting('app.encryption_key')) AS ssn
FROM "user"
WHERE id = $1;
```

**Review Checklist**:
- [ ] PII/sensitive data encrypted at rest?
- [ ] Encryption key NOT hardcoded (use session variable)?
- [ ] Decryption only when necessary?

### At Rest (Transparent Data Encryption)

**PostgreSQL 15+ (Cluster-Level Encryption)**:
```bash
# Initialize cluster with encryption
initdb -D /var/lib/postgresql/data --data-checksums --wal-init-zero --encryption=aes-256-cbc
```

**Review**: Check if TDE enabled for compliance requirements.

### In Transit (Already Covered: SSL/TLS)

---

## Common Security Anti-Patterns

### ❌ Storing Passwords in Plaintext
```sql
-- ❌ CRITICAL: Plaintext password
CREATE TABLE "user" (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    password TEXT NOT NULL -- Plaintext!
);

-- ✅ GOOD: Hashed password (bcrypt, argon2)
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE "user" (
    id UUID PRIMARY KEY,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL
);

-- Hash password on insert
INSERT INTO "user" (id, email, password_hash)
VALUES (
    gen_random_uuid(),
    'alice@example.com',
    crypt('user_password', gen_salt('bf')) -- bcrypt
);

-- Verify password
SELECT id FROM "user"
WHERE email = 'alice@example.com'
  AND password_hash = crypt('user_password', password_hash);
```

### ❌ Overly Permissive CORS
```sql
-- ❌ BAD: Allow all origins
RETURN json_build_object(
    'status', 200,
    'headers', json_build_object('Access-Control-Allow-Origin', '*'),
    'body', data
);

-- ✅ GOOD: Whitelist specific origins
RETURN json_build_object(
    'status', 200,
    'headers', json_build_object('Access-Control-Allow-Origin', 'https://app.example.com'),
    'body', data
);
```

### ❌ Mass Assignment Vulnerability
```go
// ❌ BAD: User can set any field (including role!)
type User struct {
    ID    uuid.UUID
    Email string
    Role  string // Attacker sets "role": "admin"
}

func updateUser(ctx context.Context, db *pgx.Conn, userID uuid.UUID, updates map[string]interface{}) error {
    // Directly apply all updates from user input
    for key, value := range updates {
        // Update field (dangerous!)
    }
}

// ✅ GOOD: Explicit allowed fields
type UpdateUserRequest struct {
    Email string `json:"email"` // Only email can be updated
}

func updateUser(ctx context.Context, db *pgx.Conn, userID uuid.UUID, req *UpdateUserRequest) error {
    _, err := db.Exec(ctx, `UPDATE "user" SET email = $1 WHERE id = $2`, req.Email, userID)
    return err
}
```

### ❌ Time-of-Check to Time-of-Use (TOCTOU)
```sql
-- ❌ BAD: Race condition
CREATE FUNCTION transfer_funds(p_from UUID, p_to UUID, p_amount NUMERIC)
RETURNS VOID AS $$
DECLARE
    v_balance NUMERIC;
BEGIN
    -- Check balance
    SELECT balance INTO v_balance FROM accounts WHERE id = p_from;

    IF v_balance < p_amount THEN
        RAISE EXCEPTION 'Insufficient funds';
    END IF;

    -- Time gap here! Concurrent transaction could drain account.

    -- Deduct
    UPDATE accounts SET balance = balance - p_amount WHERE id = p_from;
    UPDATE accounts SET balance = balance + p_amount WHERE id = p_to;
END;
$$ LANGUAGE plpgsql;

-- ✅ GOOD: Atomic check and update
CREATE FUNCTION transfer_funds(p_from UUID, p_to UUID, p_amount NUMERIC)
RETURNS VOID AS $$
BEGIN
    -- Atomic check + update
    UPDATE accounts
    SET balance = balance - p_amount
    WHERE id = p_from AND balance >= p_amount;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'Insufficient funds or account not found';
    END IF;

    UPDATE accounts SET balance = balance + p_amount WHERE id = p_to;
END;
$$ LANGUAGE plpgsql;
```

---

## Security Review Checklist

### SQL Injection
- [ ] No string concatenation for SQL construction with user input?
- [ ] `format(%I/%L)` used in PostgreSQL for dynamic SQL?
- [ ] Parameterized queries ($1, $2) in Go?

### Secret Handling
- [ ] Secrets NOT logged or printed?
- [ ] Secrets NOT in permanent storage?
- [ ] Secrets passed via CLI/env, not hardcoded?
- [ ] Connection strings secure (env vars, pgpass)?

### Authentication
- [ ] Authentication required for protected endpoints?
- [ ] Token expiration enforced?
- [ ] Failed auth returns 401 Unauthorized?

### Authorization
- [ ] Authorization checks after authentication?
- [ ] Principle of least privilege?
- [ ] RLS enabled for multi-tenant data?
- [ ] Failed authz returns 403 Forbidden?

### Input Validation
- [ ] Early validation before business logic?
- [ ] Type safety enforced?
- [ ] Range checks for numeric inputs?
- [ ] Path traversal prevention?

### Connection Security
- [ ] SSL/TLS required (`sslmode=require`)?
- [ ] Certificate validation enabled (`verify-ca`/`verify-full`)?
- [ ] mTLS key passphrase via `PGSSLPASSWORD` env var only (not flag/yaml)?

### Encryption
- [ ] PII/sensitive data encrypted at rest?
- [ ] Passwords hashed (bcrypt, argon2)?

### Anti-Patterns Avoided
- [ ] No plaintext passwords?
- [ ] No overly permissive CORS (*)?
- [ ] No mass assignment vulnerabilities?
- [ ] No TOCTOU race conditions?

---

**End of pgmi-security-review**

