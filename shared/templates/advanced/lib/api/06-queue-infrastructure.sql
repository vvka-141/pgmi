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

COMMENT ON TABLE api.rest_exchange IS
    'REST protocol exchanges. Inherits the log fields from api.exchange.';

CREATE TABLE api.rpc_exchange (
    request api.rpc_request NOT NULL,
    response api.http_response,
    completed_at timestamptz
) INHERITS (api.exchange);


CREATE UNIQUE INDEX uq_rpc_exchange_seq
    ON api.rpc_exchange(sequence_number);

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

COMMENT ON TABLE api.mcp_exchange IS
    'MCP protocol exchanges (tools, resources, prompts). Always complete - no pending state.';

-- Indexes are not inherited, so each child table has its own set: the
-- exchange id for lookups, enqueued_at for the newest-first admin list and
-- retention, the x-request-id header (REST, RPC), and the logged actual
-- principal for GET /admin/exchanges?actualPrincipalId=.
CREATE UNIQUE INDEX uq_rest_exchange_object_id ON api.rest_exchange (object_id);
CREATE UNIQUE INDEX uq_rpc_exchange_object_id ON api.rpc_exchange (object_id);
CREATE UNIQUE INDEX uq_mcp_exchange_object_id ON api.mcp_exchange (object_id);

CREATE INDEX ix_rest_exchange_enqueued ON api.rest_exchange (enqueued_at);
CREATE INDEX ix_rpc_exchange_enqueued ON api.rpc_exchange (enqueued_at);
CREATE INDEX ix_mcp_exchange_enqueued ON api.mcp_exchange (enqueued_at);

CREATE INDEX ix_rest_exchange_request_id
    ON api.rest_exchange (((request).headers OPERATOR(extensions.->) 'x-request-id'));
CREATE INDEX ix_rpc_exchange_request_id
    ON api.rpc_exchange (((request).headers OPERATOR(extensions.->) 'x-request-id'));

CREATE INDEX ix_rest_exchange_actual_principal ON api.rest_exchange ((context->>'actualPrincipalId'), enqueued_at);
CREATE INDEX ix_rpc_exchange_actual_principal ON api.rpc_exchange ((context->>'actualPrincipalId'), enqueued_at);
CREATE INDEX ix_mcp_exchange_actual_principal ON api.mcp_exchange ((context->>'actualPrincipalId'), enqueued_at);

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT USAGE ON SEQUENCE api.exchange_seq TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rest_exchange TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.rpc_exchange TO %I', v_admin_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON api.mcp_exchange TO %I', v_admin_role);
END $$;

SELECT pg_temp.exempt_from_rls('api.exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.rest_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.rpc_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
SELECT pg_temp.exempt_from_rls('api.mcp_exchange', 'exchange log: written by the gateways, read through platform.operate handlers');
