/*
<pgmi-meta
    id="f671bab1-42d0-45d2-aa57-ddc5de701ac9"
    idempotent="true">
  <description>
    The gateway's grant: the api role executes exactly the entry points
    listed here (plus api.open_dev_login_session with env=dev), each of which
    decides access before any handler runs. deploy/finalize.sql calls it after
    the PUBLIC sweep; lib/__test__/test_exact_grants.sql holds the list.
  </description>
  <sortKeys>
    <key>004/013</key>
  </sortKeys>
</pgmi-meta>
*/

CREATE OR REPLACE FUNCTION internal.grant_gateway_entry_points()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    v_api_role text := pg_temp.deployment_setting('database_api_role');
BEGIN
    -- The gateway executes its entry points and nothing else: each one
    -- decides access before any handler runs. The admin login needs no
    -- grant: it is a member of the owner. lib/__test__/test_exact_grants.sql
    -- holds every login to its list.
    EXECUTE format('GRANT EXECUTE ON ROUTINE '
                   'api.rest_invoke(text, text, extensions.hstore, bytea), '
                   'api.rest_invoke(text, text, extensions.hstore, jsonb), '
                   'api.rest_invoke(text, text, extensions.hstore, xml), '
                   'api.rest_route_policy(text, text, text), '
                   'api.rpc_resolve(text), '
                   'api.rpc_invoke(uuid, extensions.hstore, bytea), '
                   'api.rpc_route_policy(text, text), '
                   'api.mcp_handle_request(jsonb, jsonb), '
                   'api.mcp_request_policy(text, text, text, text, text), '
                   'api.catalog_version(), '
                   'api.open_login_session(text, text, text, text, boolean, text[]), '
                   'api.close_login_session(text) TO %I', v_api_role);
    IF current_setting('pgmi.env') = 'dev' THEN

        IF to_regprocedure('api.open_dev_login_session(text, text, text, text)') IS NOT NULL THEN
            EXECUTE format('GRANT EXECUTE ON ROUTINE api.open_dev_login_session(text, text, text, text) TO %I', v_api_role);
        END IF;
    END IF;
END $$;
