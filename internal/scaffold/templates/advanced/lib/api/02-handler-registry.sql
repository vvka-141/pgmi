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
-- Handler Registry Table
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.handler (
    object_id uuid PRIMARY KEY,
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

    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS ix_handler_type ON api.handler(handler_type);

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.handler - central handler registry with pg_proc snapshot';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.handler TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.handler TO %I', v_admin_role);
END $$;
