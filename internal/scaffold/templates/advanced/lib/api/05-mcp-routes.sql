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
    arguments jsonb,
    tags text[] NOT NULL DEFAULT '{}',

    -- api.uri_template_to_regex implements RFC 6570 Level 1 simple-string
    -- expansion only. Level 2+ operators (+ reserved, ? query, / path,
    -- . label, ; param, & form, # fragment) would silently mis-route.
    -- Reject them at registration instead of producing broken URIs.
    --
    -- Resource-routing precedence: when more than one template matches a URI,
    -- api.mcp_read_resource picks the most specific (longest uri_template) with
    -- mcp_name as a stable tiebreak, so routing is deterministic.
    CONSTRAINT ck_uri_template_level1_only CHECK (
        uri_template IS NULL
        OR uri_template !~ '\{[+?#./;&]'
    )
);

ALTER TABLE api.mcp_route ADD COLUMN IF NOT EXISTS tags text[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS ix_mcp_route_type ON api.mcp_route(mcp_type);
CREATE INDEX IF NOT EXISTS ix_mcp_route_name ON api.mcp_route(mcp_name);
CREATE INDEX IF NOT EXISTS ix_mcp_route_tags ON api.mcp_route USING GIN(tags);

COMMENT ON COLUMN api.mcp_route.tags IS
    'Freeform tags for filtering tools via api.mcp_list_tools(p_tags). GIN-indexed for efficient && (overlap) queries.';

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
