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
    auto_log boolean NOT NULL DEFAULT true
);

CREATE INDEX IF NOT EXISTS ix_rest_route_lookup
    ON api.rest_route(sequence_number DESC);

COMMENT ON TABLE api.rest_route IS
    'REST routes matched by sequence_number DESC. Later-registered routes take priority when patterns overlap.';

COMMENT ON COLUMN api.rest_route.sequence_number IS
    'Auto-incrementing priority. Higher values (later registration) match first.';

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT ON api.rest_route TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_route TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.rest_route - URL pattern matching routes';
END $$;
