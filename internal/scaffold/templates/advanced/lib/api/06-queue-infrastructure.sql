/*
<pgmi-meta
    id="a7f01000-0006-4000-8000-000000000001"
    idempotent="false">
  <description>
    Exchange log: the abstract api.exchange with one inherited table per protocol
  </description>
  <sortKeys>
    <key>004/006</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing the exchange log'; END $$;

-- ============================================================================
-- Shared Sequence (Global Ordering)
-- ============================================================================

CREATE SEQUENCE api.exchange_seq;

-- ============================================================================
-- Exchange (Abstract Base)
-- ============================================================================

CREATE TABLE api.exchange (
    sequence_number bigint NOT NULL DEFAULT nextval('api.exchange_seq'),
    object_id uuid NOT NULL DEFAULT uuidv7(),
    enqueued_at timestamptz NOT NULL DEFAULT now(),
    handler_object_id uuid NOT NULL,
    context jsonb,

    -- NO INHERIT: child tables get the columns but not this constraint, so they accept inserts normally
    CONSTRAINT exchange_is_abstract_check CHECK (false) NO INHERIT
);

COMMENT ON TABLE api.exchange IS
    'Abstract base of the exchange log: one row per logged request across protocols. A log, not a queue.';

COMMENT ON COLUMN api.exchange.sequence_number IS
    'Global sequence for ordering across all protocols. Shared via api.exchange_seq.';

COMMENT ON COLUMN api.exchange.object_id IS
    'Correlation ID for request tracking and logging.';

COMMENT ON COLUMN api.exchange.context IS
    'The request context internal.request_log_context() returned when the exchange was logged; api.replay_exchange applies it through internal.apply_request_log_context().';

CREATE TABLE api.rest_exchange (
    request api.rest_request NOT NULL,
    response api.http_response,
    completed_at timestamptz
) INHERITS (api.exchange);


CREATE UNIQUE INDEX uq_rest_exchange_seq
    ON api.rest_exchange(sequence_number);

CREATE INDEX ix_rest_exchange_pending
    ON api.rest_exchange(sequence_number)
    INCLUDE (object_id, handler_object_id, request)
    WHERE response IS NULL;

CREATE INDEX ix_rest_exchange_completed
    ON api.rest_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

COMMENT ON TABLE api.rest_exchange IS
    'REST protocol exchanges. Inherits the log fields from api.exchange.';

CREATE TABLE api.rpc_exchange (
    request api.rpc_request NOT NULL,
    response api.http_response,
    completed_at timestamptz
) INHERITS (api.exchange);


CREATE UNIQUE INDEX uq_rpc_exchange_seq
    ON api.rpc_exchange(sequence_number);

CREATE INDEX ix_rpc_exchange_pending
    ON api.rpc_exchange(sequence_number)
    INCLUDE (object_id, handler_object_id, request)
    WHERE response IS NULL;

CREATE INDEX ix_rpc_exchange_completed
    ON api.rpc_exchange(completed_at)
    WHERE completed_at IS NOT NULL;

COMMENT ON TABLE api.rpc_exchange IS
    'RPC protocol exchanges. Inherits the log fields from api.exchange.';

CREATE TABLE api.mcp_exchange (
    mcp_type text NOT NULL,
    mcp_name text NOT NULL,
    request api.mcp_request NOT NULL,
    response api.mcp_response NOT NULL,
    completed_at timestamptz NOT NULL DEFAULT now()
) INHERITS (api.exchange);


CREATE UNIQUE INDEX uq_mcp_exchange_seq
    ON api.mcp_exchange(sequence_number);

CREATE INDEX ix_mcp_exchange_completed
    ON api.mcp_exchange(completed_at);

CREATE INDEX ix_mcp_exchange_type
    ON api.mcp_exchange(mcp_type);

CREATE INDEX ix_mcp_exchange_name
    ON api.mcp_exchange(mcp_name);

COMMENT ON TABLE api.mcp_exchange IS
    'MCP protocol exchanges (tools, resources, prompts). Always complete - no pending state.';

-- Retention scans by enqueued_at; BRIN stays tiny on append-only tables.
CREATE INDEX ix_rest_exchange_enqueued
    ON api.rest_exchange USING brin (enqueued_at);

CREATE INDEX ix_rpc_exchange_enqueued
    ON api.rpc_exchange USING brin (enqueued_at);

CREATE INDEX ix_mcp_exchange_enqueued
    ON api.mcp_exchange USING brin (enqueued_at);

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT USAGE ON SEQUENCE api.exchange_seq TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SEQUENCE api.exchange_seq TO %I', v_admin_role);

    -- Exchange tables persist full request/response payloads including
    -- truncated SQLERRM for failed handlers. The api role (request execution)
    -- needs INSERT/UPDATE to log its own activity but MUST NOT be able to
    -- read other rows — otherwise a compromised api session leaks every
    -- handler exception across the deployment. Admin retains full access for
    -- ops triage.
    EXECUTE format('GRANT INSERT, UPDATE ON api.rest_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT INSERT, UPDATE ON api.rpc_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rpc_exchange TO %I', v_admin_role);

    EXECUTE format('GRANT INSERT ON api.mcp_exchange TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.mcp_exchange TO %I', v_admin_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.exchange_seq - shared sequence for global ordering';
    RAISE NOTICE '  ✓ api.exchange - abstract base (no protocol column)';
    RAISE NOTICE '  ✓ BRIN indexes on exchange enqueued_at';
    RAISE NOTICE '  ✓ api.rest_exchange - REST with partial index for SKIP LOCKED';
    RAISE NOTICE '  ✓ api.rpc_exchange - RPC with partial index for SKIP LOCKED';
    RAISE NOTICE '  ✓ api.mcp_exchange - MCP (always complete)';
END $$;

SELECT pg_temp.exempt_from_rls('api.exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.rest_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.rpc_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.mcp_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
