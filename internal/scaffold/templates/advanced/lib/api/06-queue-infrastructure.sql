/*
<pgmi-meta
    id="a7f01000-0006-4000-8000-000000000001"
    idempotent="true">
  <description>
    Queue infrastructure: unified inbound queue with protocol-specific exchange tables
  </description>
  <sortKeys>
    <key>004/006</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing queue infrastructure'; END $$;

-- ============================================================================
-- Inbound Queue (Unified)
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.inbound_queue (
    object_id uuid NOT NULL DEFAULT gen_random_uuid(),
    sequence_number bigint GENERATED ALWAYS AS IDENTITY,
    enqueued_at timestamptz NOT NULL DEFAULT now(),

    protocol text NOT NULL CHECK (protocol IN ('rest', 'rpc', 'mcp')),
    handler_object_id uuid NOT NULL REFERENCES api.handler(object_id),

    PRIMARY KEY (object_id, sequence_number),
    UNIQUE (sequence_number)
);

CREATE INDEX IF NOT EXISTS ix_inbound_queue_enqueued
    ON api.inbound_queue(enqueued_at);

-- ============================================================================
-- REST Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rest_exchange (
    queue_object_id uuid NOT NULL,
    queue_sequence_number bigint NOT NULL,

    request api.rest_request NOT NULL,
    response api.http_response,
    completed_at timestamptz,

    PRIMARY KEY (queue_object_id, queue_sequence_number),
    FOREIGN KEY (queue_object_id, queue_sequence_number)
        REFERENCES api.inbound_queue(object_id, sequence_number) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_rest_exchange_pending
    ON api.rest_exchange(queue_sequence_number)
    INCLUDE (queue_object_id)
    WHERE response IS NULL;

CREATE INDEX IF NOT EXISTS ix_rest_exchange_completed
    ON api.rest_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_rest_exchange_queue_object
    ON api.rest_exchange(queue_object_id);

-- ============================================================================
-- RPC Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.rpc_exchange (
    queue_object_id uuid NOT NULL,
    queue_sequence_number bigint NOT NULL,

    request api.rpc_request NOT NULL,
    response api.http_response,
    completed_at timestamptz,

    PRIMARY KEY (queue_object_id, queue_sequence_number),
    FOREIGN KEY (queue_object_id, queue_sequence_number)
        REFERENCES api.inbound_queue(object_id, sequence_number) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_rpc_exchange_pending
    ON api.rpc_exchange(queue_sequence_number)
    INCLUDE (queue_object_id)
    WHERE response IS NULL;

CREATE INDEX IF NOT EXISTS ix_rpc_exchange_completed
    ON api.rpc_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS ix_rpc_exchange_queue_object
    ON api.rpc_exchange(queue_object_id);

-- ============================================================================
-- MCP Exchange
-- ============================================================================

CREATE TABLE IF NOT EXISTS api.mcp_exchange (
    queue_object_id uuid NOT NULL,
    queue_sequence_number bigint NOT NULL,

    mcp_type text NOT NULL,
    mcp_name text NOT NULL,

    request api.mcp_request NOT NULL,
    response api.mcp_response NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (queue_object_id, queue_sequence_number),
    FOREIGN KEY (queue_object_id, queue_sequence_number)
        REFERENCES api.inbound_queue(object_id, sequence_number) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_completed
    ON api.mcp_exchange(completed_at);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_type
    ON api.mcp_exchange(mcp_type);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_name
    ON api.mcp_exchange(mcp_name);

CREATE INDEX IF NOT EXISTS ix_mcp_exchange_queue_object
    ON api.mcp_exchange(queue_object_id);

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
BEGIN
    EXECUTE format('GRANT SELECT, INSERT ON api.inbound_queue TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.inbound_queue TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT ON api.rest_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT ON api.rpc_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rpc_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT SELECT, INSERT ON api.mcp_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.mcp_exchange TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.inbound_queue - unified queue with protocol field';
    RAISE NOTICE '  ✓ api.rest_exchange - REST request/response storage';
    RAISE NOTICE '  ✓ api.rpc_exchange - RPC request/response storage';
    RAISE NOTICE '  ✓ api.mcp_exchange - MCP request/response storage (always complete)';
END $$;
