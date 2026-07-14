/*
<pgmi-meta
    id="a7f01000-0003-4000-8000-000000000001"
    idempotent="true">
  <description>
    REST routing: route table for URL-pattern-based HTTP routing
  </description>
  <sortKeys>
    <key>004/003</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing REST routing infrastructure'; END $$;

-- ============================================================================
-- REST Route Table
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rest_route (
    handler_object_id uuid PRIMARY KEY
        REFERENCES api.handler(object_id) ON DELETE CASCADE,

    sequence_number bigint GENERATED ALWAYS AS IDENTITY,
    address_regexp text NOT NULL,
    method_regexp text NOT NULL DEFAULT '^(GET|POST|PUT|DELETE|PATCH)$',
    version_regexp text NOT NULL DEFAULT '.*',

    route_name text,
    auto_log boolean NOT NULL DEFAULT true,
    path_param_names text[] NOT NULL DEFAULT '{}'
);

ALTER TABLE api.rest_route
    ADD COLUMN IF NOT EXISTS path_param_names text[] NOT NULL DEFAULT '{}';

CREATE INDEX IF NOT EXISTS ix_rest_route_lookup
    ON api.rest_route(sequence_number DESC);

-- route_name becomes the handler's function name (api.<name>), so two routes
-- cannot share one — the second CREATE OR REPLACE would overwrite the first's
-- body. rpc_route.method_name and mcp_route.mcp_name are both NOT NULL UNIQUE;
-- route_name was neither, and the collision was caught only incidentally by the
-- UNIQUE on api.handler.handler_func. Make the guarantee explicit and local.
-- Partial: an unnamed route (route_name IS NULL) gets a generated function name
-- and cannot collide.
CREATE UNIQUE INDEX IF NOT EXISTS uq_rest_route_name
    ON api.rest_route(route_name)
    WHERE route_name IS NOT NULL;

COMMENT ON TABLE api.rest_route IS
    'REST routes matched by sequence_number DESC. Later-registered routes take priority when patterns overlap.';

COMMENT ON COLUMN api.rest_route.sequence_number IS
    'Auto-incrementing priority. Higher values (later registration) match first.';

COMMENT ON COLUMN api.rest_route.path_param_names IS
    'Names for the address_regexp capture groups, in order, as declared by the handler''s pathParams metadata. Empty means the OpenAPI document names them positionally (p1, p2, ...).';

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.rest_route TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_route TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.rest_route - URL pattern matching routes';
END $$;
