-- ============================================================================
-- Test: declared access (lib/api/02a-access-control*.sql)
-- ============================================================================
-- Registration refuses a handler without exactly one valid declaration, and the
-- gateways refuse, log and never run a handler the caller may not call. The
-- test's permissions join the vocabulary, and the test replaces the readers
-- the decision calls; the savepoint around every test rolls that back.

INSERT INTO membership.permission (id, is_platform, description)
VALUES ('test.task.read', false, 'Test permission'), ('test.platform.ops', true, 'Test platform permission');

CREATE OR REPLACE FUNCTION api.current_organization_id()
RETURNS uuid LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('test.organization_id', true), '')::uuid;
$$;

CREATE OR REPLACE FUNCTION api.current_permissions()
RETURNS text[] LANGUAGE sql STABLE AS $$
    SELECT COALESCE(NULLIF(current_setting('test.permissions', true), '')::text[], '{}');
$$;

-- ----------------------------------------------------------------------------
-- Registration: exactly one valid declaration, or the deploy fails naming it
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_case record;
    v_kind text;
    v_meta jsonb;
    v_message text;
BEGIN
    RAISE NOTICE '-> Declared access: registration refusals';

    FOR v_case IN
        SELECT * FROM (VALUES
            ('none',          '{}'::jsonb),
            ('both',          '{"access": "public", "permissions": ["test.task.read"]}'),
            ('requiresAuth',  '{"requiresAuth": false}'),
            ('requiresAuth+', '{"requiresAuth": true, "access": "authenticated"}'),
            ('bad access',    '{"access": "everyone"}'),
            ('access array',  '{"access": ["public"]}'),
            ('empty',         '{"permissions": []}'),
            ('not array',     '{"permissions": "test.task.read"}'),
            ('not string',    '{"permissions": [1]}'),
            ('unknown',       '{"permissions": ["test.task.read", "test.nope"]}')
        ) AS c(label, declaration)
    LOOP
        FOREACH v_kind IN ARRAY ARRAY['rest', 'rpc', 'mcp'] LOOP
            v_meta := v_case.declaration || CASE v_kind
                WHEN 'rest' THEN jsonb_build_object('id', 'da000000-0000-4000-8000-000000000001',
                                                    'path', '/test-da/refused', 'name', 'da_refused_rest')
                WHEN 'rpc' THEN jsonb_build_object('id', 'da000000-0000-4000-8000-000000000002',
                                                   'methodName', 'da.refused')
                ELSE jsonb_build_object('id', 'da000000-0000-4000-8000-000000000003', 'type', 'tool', 'effect', 'read',
                                        'name', 'da_refused_tool', 'description', 'refused')
            END;
            v_message := NULL;
            BEGIN
                CASE v_kind
                    WHEN 'rest' THEN PERFORM api.create_or_replace_rest_handler(v_meta,
                        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);
                    WHEN 'rpc' THEN PERFORM api.create_or_replace_rpc_handler(v_meta,
                        $b$BEGIN RETURN NULL; END;$b$);
                    ELSE PERFORM api.create_or_replace_mcp_handler(v_meta,
                        $b$BEGIN RETURN NULL; END;$b$);
                END CASE;
            EXCEPTION WHEN invalid_parameter_value OR foreign_key_violation THEN
                v_message := SQLERRM;
            END;
            IF v_message IS NULL THEN
                RAISE EXCEPTION 'TEST FAILED: % registration accepted declaration "%": %', v_kind, v_case.label, v_case.declaration;
            END IF;
            IF (position((CASE v_kind WHEN 'rest' THEN 'da_refused_rest'
                                     WHEN 'rpc' THEN 'da.refused'
                                     ELSE 'da_refused_tool' END) IN v_message) = 0) IS NOT FALSE THEN
                RAISE EXCEPTION 'TEST FAILED: % refusal of "%" does not name the handler: %', v_kind, v_case.label, v_message;
            END IF;
        END LOOP;
    END LOOP;

    IF EXISTS (SELECT FROM api.handler WHERE object_id::text LIKE 'da000000-0000-4000-8000-00000000000_') THEN
        RAISE EXCEPTION 'TEST FAILED: a refused registration left a handler row';
    END IF;

    RAISE NOTICE '  + no declaration, both keys, requiresAuth, malformed, empty and unknown permissions are refused by REST, RPC and MCP';
END $$;

-- ----------------------------------------------------------------------------
-- The decision
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_case record;
    v_reason text;
BEGIN
    RAISE NOTICE '-> Declared access: internal.access_denial_reason';

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000010', 'path', '/test-da/unit', 'httpMethod', '^GET$',
        'name', 'da_unit', 'permissions', jsonb_build_array('test.task.read')),
        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000011', 'path', '/test-da/unit-platform', 'httpMethod', '^GET$',
        'name', 'da_unit_platform', 'permissions', jsonb_build_array('test.platform.ops')),
        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('public, anonymous',            '00000000-0000-0000-0000-000000000000'::uuid, 'public',        false, false, '{}'::text[], NULL),
            ('authenticated, anonymous',     '00000000-0000-0000-0000-000000000000', 'authenticated', false, false, '{}', 'unauthenticated'),
            ('authenticated, signed in',     '00000000-0000-0000-0000-000000000000', 'authenticated', true,  false, '{}', NULL),
            ('organization, anonymous',      '00000000-0000-0000-0000-000000000000', 'organization',  false, true,  '{}', 'unauthenticated'),
            ('organization, no organization','00000000-0000-0000-0000-000000000000', 'organization',  true,  false, '{test.platform.ops}', 'organization_required'),
            ('organization, in one',         '00000000-0000-0000-0000-000000000000', 'organization',  true,  true,  '{}', NULL),
            ('permission, anonymous',        'da000000-0000-4000-8000-000000000010', 'permission',    false, true,  '{test.task.read}', 'unauthenticated'),
            ('permission, held',             'da000000-0000-4000-8000-000000000010', 'permission',    true,  true,  '{test.task.read}', NULL),
            ('permission, no organization',  'da000000-0000-4000-8000-000000000010', 'permission',    true,  false, '{}', 'organization_required'),
            ('permission, missing',          'da000000-0000-4000-8000-000000000010', 'permission',    true,  true,  '{test.platform.ops}', 'permission_missing'),
            ('platform, no organization',    'da000000-0000-4000-8000-000000000011', 'permission',    true,  false, '{test.platform.ops}', NULL),
            ('platform, missing',            'da000000-0000-4000-8000-000000000011', 'permission',    true,  false, '{}', 'permission_missing')
        ) AS c(label, handler, access, authenticated, has_org, permissions, expected)
    LOOP
        v_reason := internal.access_denial_reason(v_case.handler, v_case.access, v_case.authenticated,
                                                  v_case.has_org, v_case.permissions);
        IF v_reason IS DISTINCT FROM v_case.expected THEN
            RAISE EXCEPTION 'TEST FAILED: %: expected %, got %', v_case.label, v_case.expected, v_reason;
        END IF;
    END LOOP;

    RAISE NOTICE '  + every reason, and every allowed path';
END $$;

-- ----------------------------------------------------------------------------
-- The gateways: refused, logged, and the handler body never ran
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_user      uuid;
    v_org       uuid := gen_random_uuid();
    v_home      uuid;
    v_mcp       jsonb;
    v_response  api.http_response;
    v_body      jsonb;
    v_envelope  jsonb;
    v_case      record;
    v_attempts  int;
    v_purged    bigint;
BEGIN
    RAISE NOTICE '-> Declared access: gateways';

    PERFORM pg_temp.test_login('test', 'declared-access', 'declared-access@example.test', NULL, true);
    PERFORM pg_temp.test_act_as('test|declared-access');
    SELECT api.current_principal_id() INTO v_user;
    PERFORM pg_temp.test_act_as(NULL);
    v_home := (membership.fn_create_organization_with_owner('Declared Access', 'declared-access', v_user)).object_id;
    v_mcp := pg_temp.test_mcp_context('test|declared-access', v_home);

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000020', 'path', '/test-da/task', 'httpMethod', '^GET$',
        'name', 'da_task', 'permissions', jsonb_build_array('test.task.read'),
        'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.json_response(200, '{}'::jsonb);
        END;$b$);
    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000021', 'methodName', 'da.task',
        'permissions', jsonb_build_array('test.task.read')),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id');
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000022', 'type', 'tool', 'effect', 'read', 'name', 'da_task_tool',
        'description', 'Permission-guarded tool', 'permissions', jsonb_build_array('test.task.read')),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id);
        END;$b$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('anonymous',             NULL,                     NULL::uuid, '{}'::text,           401, 'unauthenticated'),
            ('no organization',       'test|declared-access',   NULL,       '{}',                 403, 'organization_required'),
            ('no permission',         'test|declared-access',   v_org,      '{test.platform.ops}', 403, 'permission_missing'),
            ('permission held',       'test|declared-access',   v_org,      '{test.task.read}',   200, NULL)
        ) AS c(label, subject, org, permissions, status, reason)
    LOOP
        PERFORM set_config('test.organization_id', COALESCE(v_case.org::text, ''), true);
        PERFORM set_config('test.permissions', v_case.permissions, true);

        PERFORM set_config('test.body_ran', 'no', true);
        v_response := api.rest_invoke('GET', '/test-da/task',
            CASE WHEN v_case.subject IS NULL THEN ''::extensions.hstore
                 ELSE pg_temp.test_session_headers(v_case.subject) END, NULL::bytea);
        IF (v_response).status_code IS DISTINCT FROM v_case.status THEN
            RAISE EXCEPTION 'TEST FAILED: REST %: expected %, got %', v_case.label, v_case.status, (v_response).status_code;
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: REST %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;
        v_body := api.content_json((v_response).content);
        IF (v_case.status = 403) IS NOT FALSE AND v_body->>'code' IS DISTINCT FROM
               (CASE v_case.reason WHEN 'organization_required' THEN 'pgmi.organization_required' ELSE 'pgmi.forbidden' END) THEN
            RAISE EXCEPTION 'TEST FAILED: REST %: 403 body code %', v_case.label, v_body;
        END IF;
        IF (v_case.status = 403) IS NOT FALSE AND v_body::text LIKE '%test.task.read%' THEN
            RAISE EXCEPTION 'TEST FAILED: REST %: the refusal names the permission: %', v_case.label, v_body;
        END IF;

        PERFORM set_config('test.body_ran', 'no', true);
        v_response := api.rpc_invoke('da000000-0000-4000-8000-000000000021',
            CASE WHEN v_case.subject IS NULL THEN ''::extensions.hstore
                 ELSE pg_temp.test_session_headers(v_case.subject) END,
            convert_to('{"jsonrpc":"2.0","method":"da.task","id":1}', 'UTF8'));
        IF (v_response).status_code IS DISTINCT FROM v_case.status THEN
            RAISE EXCEPTION 'TEST FAILED: RPC %: expected %, got %: %', v_case.label, v_case.status,
                (v_response).status_code, api.content_json((v_response).content);
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: RPC %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;

        PERFORM set_config('test.body_ran', 'no', true);
        v_envelope := (api.mcp_call_tool('da_task_tool', '{}'::jsonb,
            CASE WHEN v_case.subject IS NULL THEN NULL ELSE v_mcp END,
            '"da"'::jsonb)).envelope;
        IF (v_envelope ? 'error') IS DISTINCT FROM (v_case.reason IS NOT NULL) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP %: %', v_case.label, v_envelope;
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;
        IF (api.mcp_list_tools()->'tools') @> jsonb_build_array(jsonb_build_object('name', 'da_task_tool'))
           IS DISTINCT FROM (v_case.reason IS NULL) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP %: tools/list disagrees with the call', v_case.label;
        END IF;

        IF v_case.reason IS NOT NULL AND (
            SELECT count(*) FROM api.access_denial d
            WHERE d.reason = v_case.reason
              AND d.handler_object_id IN ('da000000-0000-4000-8000-000000000020',
                                          'da000000-0000-4000-8000-000000000021',
                                          'da000000-0000-4000-8000-000000000022')
              AND d.effective_principal_object_id IS NOT DISTINCT FROM (CASE WHEN v_case.subject IS NULL THEN NULL ELSE v_user END)
              AND d.organization_object_id IS NOT DISTINCT FROM v_case.org) IS DISTINCT FROM 3 THEN
            RAISE EXCEPTION 'TEST FAILED: %: expected one logged refusal per protocol', v_case.label;
        END IF;
    END LOOP;

    PERFORM set_config('test.organization_id', '', true);
    PERFORM set_config('test.permissions', '', true);
    PERFORM api.rest_invoke('GET', '/test-da/task', ''::extensions.hstore, NULL::bytea);
    SELECT attempts INTO v_attempts FROM api.access_denial
    WHERE handler_object_id = 'da000000-0000-4000-8000-000000000020' AND reason = 'unauthenticated';
    IF v_attempts IS DISTINCT FROM 2 THEN
        RAISE EXCEPTION 'TEST FAILED: a repeated refusal within the minute should collapse to one row, attempts = %', v_attempts;
    END IF;

    UPDATE api.access_denial SET bucket = now() - interval '100 days'
    WHERE handler_object_id = 'da000000-0000-4000-8000-000000000020';
    v_purged := api.purge_access_denials();
    IF coalesce(v_purged, 0) < 3 OR EXISTS (SELECT FROM api.access_denial WHERE handler_object_id = 'da000000-0000-4000-8000-000000000020') THEN
        RAISE EXCEPTION 'TEST FAILED: purge removed % rows and left the old ones', v_purged;
    END IF;
    IF NOT EXISTS (SELECT FROM api.access_denial WHERE handler_object_id = 'da000000-0000-4000-8000-000000000021') THEN
        RAISE EXCEPTION 'TEST FAILED: purge removed recent refusals';
    END IF;

    RAISE NOTICE '  + REST, RPC and MCP refuse with 401/403, log once per refusal, collapse repeats, and never run the body';
END $$;

-- ----------------------------------------------------------------------------
-- Organization access: signed in, in an organization session, no permission
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_user     uuid;
    v_org      uuid := gen_random_uuid();
    v_home     uuid;
    v_mcp      jsonb;
    v_response api.http_response;
    v_body     jsonb;
    v_envelope jsonb;
    v_case     record;
BEGIN
    RAISE NOTICE '-> Declared access: organization';

    PERFORM pg_temp.test_login('test', 'declared-org', 'declared-org@example.test', NULL, true);
    PERFORM pg_temp.test_act_as('test|declared-org');
    SELECT api.current_principal_id() INTO v_user;
    PERFORM pg_temp.test_act_as(NULL);
    v_home := (membership.fn_create_organization_with_owner('Declared Org', 'declared-org', v_user)).object_id;
    v_mcp := pg_temp.test_mcp_context('test|declared-org', v_home);

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000040', 'path', '/test-da/org', 'httpMethod', '^GET$',
        'name', 'da_org', 'access', 'organization',
        'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.json_response(200, '{}'::jsonb);
        END;$b$);
    PERFORM api.create_or_replace_rpc_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000041', 'methodName', 'da.org', 'access', 'organization'),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.jsonrpc_success('{}'::jsonb, api.content_json((request).content)->'id');
        END;$b$);
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000042', 'type', 'tool', 'effect', 'read', 'name', 'da_org_tool',
        'description', 'Organization-scoped tool', 'access', 'organization'),
        $b$BEGIN
            PERFORM set_config('test.body_ran', 'yes', true);
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('ran')), (request).request_id);
        END;$b$);

    IF (SELECT access FROM api.handler WHERE object_id = 'da000000-0000-4000-8000-000000000040') IS DISTINCT FROM 'organization' THEN
        RAISE EXCEPTION 'TEST FAILED: "access": "organization" is not stored as organization';
    END IF;
    IF EXISTS (SELECT FROM api.handler_permission WHERE handler_object_id::text LIKE 'da000000-0000-4000-8000-00000000004_') THEN
        RAISE EXCEPTION 'TEST FAILED: organization access stored a permission';
    END IF;

    FOR v_case IN
        SELECT * FROM (VALUES
            ('anonymous',       NULL,               NULL::uuid, 401, 'unauthenticated'),
            ('no organization', 'test|declared-org', NULL,       403, 'organization_required'),
            ('in organization', 'test|declared-org', v_org,      200, NULL)
        ) AS c(label, subject, org, status, reason)
    LOOP
        PERFORM set_config('test.organization_id', COALESCE(v_case.org::text, ''), true);
        PERFORM set_config('test.permissions', '', true);

        PERFORM set_config('test.body_ran', 'no', true);
        v_response := api.rest_invoke('GET', '/test-da/org',
            CASE WHEN v_case.subject IS NULL THEN ''::extensions.hstore
                 ELSE pg_temp.test_session_headers(v_case.subject) END, NULL::bytea);
        v_body := api.content_json((v_response).content);
        IF (v_response).status_code IS DISTINCT FROM v_case.status THEN
            RAISE EXCEPTION 'TEST FAILED: REST organization %: expected %, got % %', v_case.label, v_case.status,
                (v_response).status_code, v_body;
        END IF;
        IF v_case.status IS NOT DISTINCT FROM 403 AND v_body->>'code' IS DISTINCT FROM 'pgmi.organization_required' THEN
            RAISE EXCEPTION 'TEST FAILED: REST organization %: 403 body code %', v_case.label, v_body;
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: REST organization %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;

        PERFORM set_config('test.body_ran', 'no', true);
        v_response := api.rpc_invoke('da000000-0000-4000-8000-000000000041',
            CASE WHEN v_case.subject IS NULL THEN ''::extensions.hstore
                 ELSE pg_temp.test_session_headers(v_case.subject) END,
            convert_to('{"jsonrpc":"2.0","method":"da.org","id":1}', 'UTF8'));
        v_body := api.content_json((v_response).content);
        IF (v_response).status_code IS DISTINCT FROM v_case.status THEN
            RAISE EXCEPTION 'TEST FAILED: RPC organization %: expected %, got % %', v_case.label, v_case.status,
                (v_response).status_code, v_body;
        END IF;
        IF v_case.status IS NOT DISTINCT FROM 403 AND v_body->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.organization_required' THEN
            RAISE EXCEPTION 'TEST FAILED: RPC organization %: 403 error code %', v_case.label, v_body;
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: RPC organization %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;

        PERFORM set_config('test.body_ran', 'no', true);
        v_envelope := (api.mcp_call_tool('da_org_tool', '{}'::jsonb,
            CASE WHEN v_case.subject IS NULL THEN NULL ELSE v_mcp END,
            '"da-org"'::jsonb)).envelope;
        IF (v_envelope ? 'error') IS DISTINCT FROM (v_case.reason IS NOT NULL) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP organization %: %', v_case.label, v_envelope;
        END IF;
        IF v_case.status IS NOT DISTINCT FROM 403 AND v_envelope->'error'->'data'->>'code' IS DISTINCT FROM 'pgmi.organization_required' THEN
            RAISE EXCEPTION 'TEST FAILED: MCP organization %: error code %', v_case.label, v_envelope;
        END IF;
        IF current_setting('test.body_ran') IS DISTINCT FROM (CASE WHEN v_case.reason IS NULL THEN 'yes' ELSE 'no' END) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP organization %: handler body ran = %', v_case.label, current_setting('test.body_ran');
        END IF;
        IF (api.mcp_list_tools()->'tools') @> jsonb_build_array(jsonb_build_object('name', 'da_org_tool'))
           IS DISTINCT FROM (v_case.reason IS NULL) THEN
            RAISE EXCEPTION 'TEST FAILED: MCP organization %: tools/list disagrees with the call', v_case.label;
        END IF;

        IF v_case.reason IS NOT NULL AND (
            SELECT count(*) FROM api.access_denial d
            WHERE d.reason = v_case.reason
              AND d.handler_object_id IN ('da000000-0000-4000-8000-000000000040',
                                          'da000000-0000-4000-8000-000000000041',
                                          'da000000-0000-4000-8000-000000000042')) IS DISTINCT FROM 3 THEN
            RAISE EXCEPTION 'TEST FAILED: organization %: expected one logged refusal per protocol', v_case.label;
        END IF;
    END LOOP;

    PERFORM set_config('test.organization_id', '', true);
    RAISE NOTICE '  + REST, RPC and MCP admit organization access only in an organization session: 401, 403 pgmi.organization_required, 200';
END $$;

-- ----------------------------------------------------------------------------
-- Published: OpenAPI lists the permissions and the 403
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_operation jsonb;
BEGIN
    RAISE NOTICE '-> Declared access: OpenAPI';

    PERFORM pg_temp.test_login('test', 'declared-access-doc', 'declared-access-doc@example.test', NULL, true);
    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000030', 'path', '/test-da/doc', 'httpMethod', '^GET$',
        'name', 'da_doc', 'permissions', jsonb_build_array('test.task.read', 'test.platform.ops'),
        'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);

    PERFORM api.create_or_replace_rest_handler(jsonb_build_object(
        'id', 'da000000-0000-4000-8000-000000000031', 'path', '/test-da/doc-org', 'httpMethod', '^GET$',
        'name', 'da_doc_org', 'access', 'organization',
        'outputSchema', jsonb_build_object('type', 'object')),
        $b$BEGIN RETURN api.json_response(200, '{}'::jsonb); END;$b$);

    PERFORM pg_temp.test_act_as('test|declared-access-doc');
    v_operation := api.openapi_document()->'paths'->'/test-da/doc-org'->'get';
    IF v_operation->'security' IS DISTINCT FROM '[{"loginSession": [], "organizationSession": []}]'::jsonb
       OR v_operation ? 'x-pgmi-permissions' THEN
        RAISE EXCEPTION 'TEST FAILED: organization access takes both sessions and names no permission: %', v_operation;
    END IF;
    v_operation := api.openapi_document()->'paths'->'/test-da/doc'->'get';
    PERFORM pg_temp.test_act_as(NULL);

    IF v_operation->'x-pgmi-permissions' IS DISTINCT FROM '["test.platform.ops", "test.task.read"]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: x-pgmi-permissions = %', v_operation->'x-pgmi-permissions';
    END IF;
    IF v_operation->'security' IS DISTINCT FROM
           '[{"loginSession": [], "organizationSession": []}, {"loginSession": []}]'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: an organization or platform permission takes both sessions, or the login session alone: %',
            v_operation->'security';
    END IF;
    IF (v_operation->'responses' ? '401' AND v_operation->'responses' ? '403') IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: a permission-guarded operation must publish 401 and 403: %', v_operation->'responses';
    END IF;
    IF (api.openapi_document()->'paths') ? '/test-da/doc' THEN
        RAISE EXCEPTION 'TEST FAILED: an anonymous caller sees a non-public operation';
    END IF;

    RAISE NOTICE '  + x-pgmi-permissions, 401 and 403 published; hidden from anonymous callers';
END $$;
