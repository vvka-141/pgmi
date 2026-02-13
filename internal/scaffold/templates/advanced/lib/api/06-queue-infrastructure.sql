/*
<pgmi-meta
    id="a7f01000-0006-4000-8000-000000000001"
    idempotent="true">
  <description>
    Queue infrastructure: abstract inbound queue with inherited protocol exchanges
  </description>
  <sortKeys>
    <key>004/006</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing queue infrastructure'; END $$;

-- ============================================================================
-- Shared Sequence (Global Ordering)
-- ============================================================================

CREATE SEQUENCE IF NOT EXISTS api.inbound_queue_seq;

-- ============================================================================
-- Inbound Queue (Abstract Base)
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.inbound_queue (
    sequence_number bigint NOT NULL DEFAULT nextval('api.inbound_queue_seq'),
    object_id uuid NOT NULL DEFAULT gen_random_uuid(),
    enqueued_at timestamptz NOT NULL DEFAULT now(),
    handler_object_id uuid NOT NULL,

    CONSTRAINT is_abstract CHECK (false) NO INHERIT
);

COMMENT ON TABLE api.inbound_queue IS
    'Abstract base for protocol exchanges. Query this table to see all pending items across protocols.';

COMMENT ON COLUMN api.inbound_queue.sequence_number IS
    'Global sequence for ordering across all protocols. Shared via api.inbound_queue_seq.';

COMMENT ON COLUMN api.inbound_queue.object_id IS
    'Correlation ID for request tracking and logging.';

-- ============================================================================
-- REST Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rest_exchange (
    request api.rest_request NOT NULL,
    response api.http_response,
    completed_at timestamptz
) INHERITS (api.inbound_queue);


CREATE UNIQUE INDEX IF NOT EXISTS ix_rest_exchange_seq
    ON api.rest_exchange(sequence_number);

CREATE INDEX IF NOT EXISTS ix_rest_exchange_pending
    ON api.rest_exchange(sequence_number)
    INCLUDE (object_id, handler_object_id, request)
    WHERE response IS NULL;

CREATE INDEX IF NOT EXISTS ix_rest_exchange_completed
    ON api.rest_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

COMMENT ON TABLE api.rest_exchange IS
    'REST protocol exchanges. Inherits queue fields from api.inbound_queue.';

-- ============================================================================
-- RPC Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rpc_exchange (
    request api.rpc_request NOT NULL,
    response api.http_response,
    completed_at timestamptz
) INHERITS (api.inbound_queue);


CREATE UNIQUE INDEX IF NOT EXISTS ix_rpc_exchange_seq
    ON api.rpc_exchange(sequence_number);

CREATE INDEX IF NOT EXISTS ix_rpc_exchange_pending
    ON api.rpc_exchange(sequence_number)
    INCLUDE (object_id, handler_object_id, request)
    WHERE response IS NULL;

CREATE INDEX IF NOT EXISTS ix_rpc_exchange_completed
    ON api.rpc_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

COMMENT ON TABLE api.rpc_exchange IS
    'RPC protocol exchanges. Inherits queue fields from api.inbound_queue.';

-- ============================================================================
-- MCP Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.mcp_exchange (
    mcp_type text NOT NULL,
    mcp_name text NOT NULL,
    request api.mcp_request NOT NULL,
    response api.mcp_response NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now()
) INHERITS (api.inbound_queue);


CREATE UNIQUE INDEX IF NOT EXISTS ix_mcp_exchange_seq
    ON api.mcp_exchange(sequence_number);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_completed
    ON api.mcp_exchange(completed_at);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_type
    ON api.mcp_exchange(mcp_type);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_name
    ON api.mcp_exchange(mcp_name);

COMMENT ON TABLE api.mcp_exchange IS
    'MCP protocol exchanges (tools, resources, prompts). Always complete - no pending state.';

-- ============================================================================
-- Helper View: Queue with Protocol
-- ============================================================================

CREATE OR REPLACE VIEW api.inbound_queue_with_protocol AS
SELECT
    CASE tableoid
        WHEN 'api.rest_exchange'::regclass THEN 'rest'
        WHEN 'api.rpc_exchange'::regclass THEN 'rpc'
        WHEN 'api.mcp_exchange'::regclass THEN 'mcp'
    END AS protocol,
    tableoid::regclass::text AS table_name,
    sequence_number,
    object_id,
    enqueued_at,
    handler_object_id
FROM api.inbound_queue;

COMMENT ON VIEW api.inbound_queue_with_protocol IS
    'Queue view with protocol discrimination using tableoid. Use this when protocol filtering is needed.';

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT USAGE ON SEQUENCE api.inbound_queue_seq TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SEQUENCE api.inbound_queue_seq TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON api.rest_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON api.rpc_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rpc_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT ON api.mcp_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.mcp_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT ON api.inbound_queue_with_protocol TO %I', v_api_role);
    EXECUTE format('GRANT SELECT ON api.inbound_queue_with_protocol TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.inbound_queue_seq - shared sequence for global ordering';
    RAISE NOTICE '  ✓ api.inbound_queue - abstract base (no protocol column)';
    RAISE NOTICE '  ✓ api.inbound_queue_with_protocol - view with tableoid-based protocol';
    RAISE NOTICE '  ✓ api.rest_exchange - REST with partial index for SKIP LOCKED';
    RAISE NOTICE '  ✓ api.rpc_exchange - RPC with partial index for SKIP LOCKED';
    RAISE NOTICE '  ✓ api.mcp_exchange - MCP (always complete)';
END $$;
