/*
<pgmi-meta
    id="a7f01000-0004-4000-8000-000000000001"
    idempotent="true">
  <description>
    RPC routing: route table for method-name-based routing
  </description>
  <sortKeys>
    <key>004/004</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing RPC routing infrastructure'; END $$;

-- ============================================================================
-- RPC Route Table
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rpc_route (
    handler_object_id uuid PRIMARY KEY
        REFERENCES api.handler(object_id) ON DELETE CASCADE,

    method_name text NOT NULL UNIQUE,
    auto_log boolean NOT NULL DEFAULT true
);

CREATE INDEX IF NOT EXISTS ix_rpc_route_method
    ON api.rpc_route(method_name);

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.rpc_route TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rpc_route TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.rpc_route - method name based routing';
END $$;
