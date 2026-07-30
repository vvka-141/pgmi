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
    uri_regexp text,
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
ALTER TABLE api.mcp_route ADD COLUMN IF NOT EXISTS uri_regexp text;

CREATE INDEX IF NOT EXISTS ix_mcp_route_type ON api.mcp_route(mcp_type);
-- mcp_name already has a unique B-tree from the UNIQUE constraint; no extra index.
CREATE INDEX IF NOT EXISTS ix_mcp_route_tags ON api.mcp_route USING GIN(tags);

COMMENT ON TABLE api.mcp_route IS
    'Model Context Protocol routing. Stores tool, resource, and prompt metadata for MCP dispatch and discovery.';
COMMENT ON COLUMN api.mcp_route.mcp_type IS
    'Protocol capability type: tool, resource, or prompt. Determines which gateway function handles requests.';
COMMENT ON COLUMN api.mcp_route.mcp_name IS
    'Unique capability name exposed to MCP clients. Used for dispatch and in list responses.';
COMMENT ON COLUMN api.mcp_route.input_schema IS
    'JSON Schema describing expected arguments for tools. Sent to clients in tools/list for input validation.';
COMMENT ON COLUMN api.mcp_route.uri_template IS
    'RFC 6570 Level 1 URI template for resources. Used by api.mcp_read_resource to match incoming URIs.';
COMMENT ON COLUMN api.mcp_route.uri_regexp IS
    'Derived from uri_template by api.uri_template_to_regex, kept in sync by a trigger (see lib/api/07-helpers.sql). Resource dispatch matches against this so the conversion runs once at registration, not on every resources/read.';
COMMENT ON COLUMN api.mcp_route.mime_type IS
    'MIME type for resource responses. Defaults to application/json.';
COMMENT ON COLUMN api.mcp_route.arguments IS
    'Argument metadata for prompts. JSON array of {name, description, required} sent to clients in prompts/list.';
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
