/*
<pgmi-meta
    id="4b0e1c2d-8f3a-4e57-9d61-2a7c5e9f0b13"
    idempotent="false">
  <description>
    Exchange log, part 2: the logged requests hold every organization's
    bodies, so the log is forced to row-level security with one policy for
    the owner, which the gateways write it as and the admin login reads it
    through. Any other role granted SELECT on it, such as a managed
    provider's reader, sees no rows. The organization and the actual
    principal become stored columns, because the page by actual principal
    must stay an index scan under row-level security, and an index scan
    there takes only a leakproof comparison (uuid =, not jsonb ->>).
  </description>
  <sortKeys>
    <key>004/006/001</key>
  </sortKeys>
</pgmi-meta>
*/

ALTER TABLE api.exchange
    ADD COLUMN organization_id uuid GENERATED ALWAYS AS ((context->>'organizationId')::uuid) STORED,
    ADD COLUMN actual_principal_id uuid GENERATED ALWAYS AS ((context->>'actualPrincipalId')::uuid) STORED;

COMMENT ON COLUMN api.exchange.organization_id IS
    'The organization the request worked in, from context.';
COMMENT ON COLUMN api.exchange.actual_principal_id IS
    'The principal that made the request (a connection''s approver for an agent), from context. GET /admin/exchanges?actualPrincipalId= filters on it.';

DROP INDEX api.ix_rest_exchange_actual_principal, api.ix_rpc_exchange_actual_principal, api.ix_mcp_exchange_actual_principal;
CREATE INDEX ix_rest_exchange_actual_principal ON api.rest_exchange (actual_principal_id, enqueued_at);
CREATE INDEX ix_rpc_exchange_actual_principal ON api.rpc_exchange (actual_principal_id, enqueued_at);
CREATE INDEX ix_mcp_exchange_actual_principal ON api.mcp_exchange (actual_principal_id, enqueued_at);

DO $$
DECLARE
    v_table text;
BEGIN
    FOREACH v_table IN ARRAY ARRAY['exchange', 'rest_exchange', 'rpc_exchange', 'mcp_exchange'] LOOP
        EXECUTE format('ALTER TABLE api.%I ENABLE ROW LEVEL SECURITY', v_table);
        EXECUTE format('ALTER TABLE api.%I FORCE ROW LEVEL SECURITY', v_table);
        EXECUTE format('CREATE POLICY %I ON api.%I TO %I USING (true) WITH CHECK (true)',
                       v_table || '_owner', v_table, pg_temp.deployment_setting('database_owner_role'));
    END LOOP;
END $$;

DELETE FROM internal.rls_exemption
WHERE relation IN ('api.exchange'::regclass, 'api.rest_exchange'::regclass, 'api.rpc_exchange'::regclass, 'api.mcp_exchange'::regclass);
