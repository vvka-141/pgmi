-- ============================================================================
-- Test: a route that answers with a credential leaves no trace in the log
-- ============================================================================
-- POST /connections and POST /organization-sessions return a bearer handle.
-- The database keeps only its hash; the exchange log must not keep the handle.
-- ============================================================================

DO $$
DECLARE
    v_acme     uuid := current_setting('test.acme_org_id')::uuid;
    v_web      extensions.hstore;
    v_before   bigint;
    v_response api.http_response;
    v_handle   text;
BEGIN
    RAISE DEBUG '-> credentials are not logged';

    v_web := pg_temp.test_session_headers(current_setting('test.alice_subject'), v_acme, ARRAY['pwd', 'mfa']);
    SELECT count(*) INTO v_before FROM api.rest_exchange;

    v_response := api.rest_invoke('POST', '/connections', v_web, jsonb_build_object('clientName', 'Log probe', 'organizationId', v_acme, 'roleId', 'agent.reader'));
    v_handle := api.content_json((v_response).content)->>'connection';
    IF (v_response).status_code IS DISTINCT FROM 201 OR v_handle IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: POST /connections expected 201 with a handle, got %', (v_response).status_code;
    END IF;
    IF EXISTS (SELECT FROM api.rest_exchange e
               WHERE position(convert_to(v_handle, 'UTF8') IN coalesce((e.response).content, ''::bytea)) > 0
                  OR position(convert_to(v_handle, 'UTF8') IN coalesce((e.request).content, ''::bytea)) > 0) THEN
        RAISE EXCEPTION 'TEST FAILED: the connection handle was written to api.rest_exchange';
    END IF;

    v_response := api.rest_invoke('POST', '/organization-sessions', v_web - 'x-organization-session'::text,
        jsonb_build_object('organizationId', v_acme));
    v_handle := api.content_json((v_response).content)->>'organizationSession';
    IF (v_response).status_code IS DISTINCT FROM 201 OR v_handle IS NULL THEN
        RAISE EXCEPTION 'TEST FAILED: POST /organization-sessions expected 201 with a handle, got %', (v_response).status_code;
    END IF;
    IF EXISTS (SELECT FROM api.rest_exchange e
               WHERE position(convert_to(v_handle, 'UTF8') IN coalesce((e.response).content, ''::bytea)) > 0) THEN
        RAISE EXCEPTION 'TEST FAILED: the organization session handle was written to api.rest_exchange';
    END IF;

    IF (SELECT count(*) FROM api.rest_exchange) IS DISTINCT FROM v_before THEN
        RAISE EXCEPTION 'TEST FAILED: a credential-minting route wrote % exchange row(s)',
            (SELECT count(*) FROM api.rest_exchange) - v_before;
    END IF;
END $$;
