/*
<pgmi-meta
    id="a7f01000-0002-4000-8000-000000000001"
    idempotent="true">
  <description>
    Handler registry: central table for all protocol handlers with pg_proc snapshot
  </description>
  <sortKeys>
    <key>004/002</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing handler registry'; END $$;

-- ============================================================================
-- Handler Registry Table (inherits identity from core.entity)
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.handler (
    handler_type api.handler_type NOT NULL,
    handler_func regprocedure NOT NULL UNIQUE,
    handler_function_name text NOT NULL,

    accepts text[] NOT NULL DEFAULT ARRAY['*/*'],
    produces text[] NOT NULL DEFAULT ARRAY['application/json'],
    response_headers jsonb NOT NULL DEFAULT '{}',
    requires_auth boolean NOT NULL DEFAULT true,

    handler_exec_sql text NOT NULL,
    handler_sql_submitted text NOT NULL,
    handler_sql_canonical text NOT NULL,
    def_hash bytea NOT NULL,

    returns_type regtype NOT NULL,
    returns_set boolean NOT NULL,
    volatility text NOT NULL CHECK (volatility IN ('immutable','stable','volatile')),
    parallel text NOT NULL CHECK (parallel IN ('safe','restricted','unsafe')),
    leakproof boolean NOT NULL,
    security text NOT NULL CHECK (security IN ('definer','invoker')),
    language_name text NOT NULL,
    owner_name name NOT NULL,

    title text,
    description text
) INHERITS (core.entity);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'api.handler'::regclass AND contype = 'p'
    ) THEN
        ALTER TABLE api.handler ADD PRIMARY KEY (object_id);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS ix_handler_type ON api.handler(handler_type);

-- ============================================================================
-- Table and Column Documentation
-- ============================================================================

COMMENT ON TABLE api.handler IS
    'Central registry for all protocol handlers (REST, RPC, MCP). Captures pg_proc metadata at registration time as immutable snapshot. Inherits identity from core.entity.';

COMMENT ON COLUMN api.handler.handler_type IS
    'Protocol type: rest, rpc, mcp_tool, mcp_resource, mcp_prompt, queue';

COMMENT ON COLUMN api.handler.handler_func IS
    'OID reference to pg_proc (regprocedure). Used for function existence checks and introspection.';

COMMENT ON COLUMN api.handler.handler_function_name IS
    'Fully qualified function name (schema.function) for display and debugging.';

COMMENT ON COLUMN api.handler.accepts IS
    'MIME types this handler accepts. Default: */*';

COMMENT ON COLUMN api.handler.produces IS
    'MIME types this handler produces. Default: application/json';

COMMENT ON COLUMN api.handler.response_headers IS
    'Additional HTTP headers to include in responses (JSONB object).';

COMMENT ON COLUMN api.handler.requires_auth IS
    'Whether authentication is required before invocation.';

COMMENT ON COLUMN api.handler.handler_exec_sql IS
    'Executable SQL statement generated for handler invocation at runtime.';

COMMENT ON COLUMN api.handler.handler_sql_submitted IS
    'Original SQL submitted during registration (for debugging and auditing).';

COMMENT ON COLUMN api.handler.handler_sql_canonical IS
    'Canonicalized function definition from pg_get_functiondef() at registration time.';

COMMENT ON COLUMN api.handler.def_hash IS
    'SHA-256 hash of handler_sql_canonical. Used to detect definition drift.';

COMMENT ON COLUMN api.handler.returns_type IS
    'Return type OID from pg_proc snapshot (regtype).';

COMMENT ON COLUMN api.handler.returns_set IS
    'True if function returns SETOF (multiple rows).';

COMMENT ON COLUMN api.handler.volatility IS
    'Function volatility: immutable (deterministic), stable (reads DB), or volatile (may modify).';

COMMENT ON COLUMN api.handler.parallel IS
    'Parallel safety: safe (can run in parallel), restricted (leader only), or unsafe (not parallel safe).';

COMMENT ON COLUMN api.handler.leakproof IS
    'True if function is LEAKPROOF (no side-channel data leakage via errors or timing).';

COMMENT ON COLUMN api.handler.security IS
    'Execution context: definer (runs as function owner) or invoker (runs as calling user).';

COMMENT ON COLUMN api.handler.language_name IS
    'Implementation language: sql, plpgsql, plv8, etc.';

COMMENT ON COLUMN api.handler.owner_name IS
    'Role that owns the handler function.';

COMMENT ON COLUMN api.handler.description IS
    'Human-readable description (typically from pg_description or handler metadata).';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.handler - central handler registry with pg_proc snapshot';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.handler TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.handler TO %I', v_admin_role);
END $$;
