/*
<pgmi-meta
    id="a7f01000-0005-4000-8000-000000000001"
    idempotent="true">
  <description>
    MCP routing: route table for Model Context Protocol tools, resources, and prompts
  </description>
  <sortKeys>
    <key>004/005</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing MCP routing infrastructure'; END $$;

-- ============================================================================
-- MCP Route Table
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.mcp_route (
    handler_object_id uuid PRIMARY KEY
        REFERENCES api.handler(object_id) ON DELETE CASCADE,

    mcp_type text NOT NULL CHECK (mcp_type IN ('tool', 'resource', 'prompt')),
    mcp_name text NOT NULL UNIQUE,

    input_schema jsonb,
    uri_template text,
    mime_type text DEFAULT 'application/json',
    arguments jsonb
);

CREATE INDEX IF NOT EXISTS ix_mcp_route_type ON api.mcp_route(mcp_type);
CREATE INDEX IF NOT EXISTS ix_mcp_route_name ON api.mcp_route(mcp_name);

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.mcp_route TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.mcp_route TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.mcp_route - MCP tool/resource/prompt metadata';
END $$;
