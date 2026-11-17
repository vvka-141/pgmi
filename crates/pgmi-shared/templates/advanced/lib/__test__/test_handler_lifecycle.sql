-- ============================================================================
-- Test: a registered handler answers and is logged; the gateway enforces
-- Accept and Content-Type; registration refuses unsafe or colliding names
-- ============================================================================

-- ############################################################################
-- LIFECYCLE - a registered handler answers its route and lands in the exchange log
-- ############################################################################
DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-0001-4000-8000-000000000001';
    v_response api.http_response;
    v_logged     int;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'path', '/test-lifecycle',
            'httpMethod', '^GET$',
            'name', 'test_lifecycle',
            'description', 'Lifecycle test handler',
            'autoLog', true,
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('lifecycle', 'ok'));
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/test-lifecycle', ''::extensions.hstore, NULL::bytea);

    PERFORM pg_temp.expect_status('Handler invocation failed', v_response, 200);

    SELECT COUNT(*) INTO v_logged
    FROM api.exchange
    WHERE handler_object_id = v_handler_id;

    IF v_logged IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'Expected 1 exchange entry, got %', v_logged;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM api.rest_exchange
        WHERE handler_object_id = v_handler_id
    ) THEN
        RAISE EXCEPTION 'REST exchange not logged';
    END IF;
END $$;

-- ############################################################################
-- CONTENT NEGOTIATION - an Accept the route cannot produce is 406, a body type
-- it does not take is 415
-- ############################################################################
DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-0002-4000-8000-000000000001';
    v_response api.http_response;
BEGIN
    -- Register a handler for content negotiation tests
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'path', '/test-content-nego',
            'httpMethod', '^GET$',
            'name', 'test_content_nego',
            'description', 'Content negotiation test handler',
            'autoLog', false,
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('nego', 'ok'));
END;
        $body$
    );

    -- Accept: application/json -> 200 (matches default produces)
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/json'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept: application/json should return 200', v_response, 200);

    -- Accept: */* -> 200 (wildcard)
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>*/*'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept: */* should return 200', v_response, 200);

    -- Accept: application/xml -> 406 (not supported)
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/xml'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept: application/xml should return 406', v_response, 406);

    -- handler.accepts enforcement -> 415 on unsupported request body type
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', 'dddddddd-0050-4000-8000-000000000001',
            'path', '/test-accepts',
            'httpMethod', '^POST$',
            'name', 'test_accepts',
            'description', 'Handler that only accepts application/json bodies',
            'autoLog', false,
            'access', 'public',
            'accepts', jsonb_build_array('application/json')
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('ok', true));
END;
        $body$
    );

    v_response := api.rest_invoke(
        'POST', '/test-accepts',
        extensions.hstore('content-type', 'application/json; charset=utf-8'),
        convert_to('{}', 'UTF8')
    );
    PERFORM pg_temp.expect_status('Content-Type application/json should be accepted (200)', v_response, 200);

    v_response := api.rest_invoke(
        'POST', '/test-accepts',
        extensions.hstore('content-type', 'application/xml'),
        convert_to('<x/>', 'UTF8')
    );
    PERFORM pg_temp.expect_status('Content-Type application/xml should return 415', v_response, 415);

    -- No Accept header -> 200 (no preference)
    v_response := api.rest_invoke('GET', '/test-content-nego', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('No Accept header should return 200', v_response, 200);

    -- Accept with multiple types including json -> 200
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        '"accept"=>"text/html, application/json, text/plain"'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept with json in list should return 200', v_response, 200);

    -- Media-range Accept must not false-406, and a superstring
    -- subtype must not false-accept.

    -- Accept: application/* matches a JSON-producing handler (no false 406).
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/*'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept: application/* should match application/json (200)', v_response, 200);

    -- Accept: application/json-patch+json must NOT match application/json.
    v_response := api.rest_invoke(
        'GET', '/test-content-nego',
        'accept=>application/json-patch+json'::extensions.hstore,
        NULL::bytea
    );
    PERFORM pg_temp.expect_status('Accept: application/json-patch+json must NOT match application/json', v_response, 406);
END $$;

-- ############################################################################
-- QUERY STRINGS - route resolution strips the query string before the path
-- regex matches
-- ############################################################################
DO $$
DECLARE
    v_handler_id uuid := 'ffffffff-c004-4000-8000-000000000001';
    v_response api.http_response;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_handler_id,
            'path', '/qs-probe',  -- No '(\?.*)?$' needed; gateway strips query string
            'httpMethod', '^GET$',
            'name', 'qs_probe',
            'access', 'public'
        ),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('matched', true));
END;
        $body$
    );

    v_response := api.rest_invoke('GET', '/qs-probe', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('route should match bare path', v_response, 200);

    v_response := api.rest_invoke('GET', '/qs-probe?foo=bar&baz=quux', ''::extensions.hstore, NULL::bytea);
    PERFORM pg_temp.expect_status('route should match path with query string (gateway strips it)', v_response, 200);
END $$;

-- ############################################################################
-- NAMES - registration refuses a name that would be truncated as an identifier,
-- or is empty; a rename drops the old function
-- ############################################################################
DO $$
DECLARE
    v_caught boolean := false;
BEGIN
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-c005-4000-8000-000000000001',
                'path', '/long',
                'httpMethod', '^GET$',
                -- 60 chars - over the 49 limit
                'name', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
                'access', 'public'
            ),
            'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
        );
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM ~ 'invalid handler name' THEN
            v_caught := true;
        ELSE
            RAISE;
        END IF;
    END;

    IF v_caught IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'expected handler-name validation error for 60-char name';
    END IF;

    -- Empty name (rejected by IS NULL check or regex)
    v_caught := false;
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object(
                'id', 'ffffffff-c006-4000-8000-000000000001',
                'path', '/empty',
                'httpMethod', '^GET$',
                'name', '',
                'access', 'public'
            ),
            'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
        );
    EXCEPTION WHEN OTHERS THEN
        IF SQLERRM ~ 'handler name|invalid handler name' THEN
            v_caught := true;
        ELSE
            RAISE;
        END IF;
    END;

    IF v_caught IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'expected validation error for empty handler name';
    END IF;
END $$;

DO $$
DECLARE
    v_id uuid := 'ffffffff-c007-4000-8000-000000000001';
    v_alpha_exists boolean;
    v_beta_exists boolean;
    v_registered text;
BEGIN
    -- Register under the first name
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_id, 'path', '/rename-test', 'httpMethod', '^GET$',
            'name', 'rename_alpha', 'access', 'public'
        ),
        'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
    );

    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_alpha'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_alpha_exists;
    IF v_alpha_exists IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'rename test setup: api.rename_alpha should exist after first registration';
    END IF;

    -- Re-register the SAME id under a new name
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object(
            'id', v_id, 'path', '/rename-test', 'httpMethod', '^GET$',
            'name', 'rename_beta', 'access', 'public'
        ),
        'BEGIN RETURN api.json_response(200, ''{}''::jsonb); END;'
    );

    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_alpha'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_alpha_exists;
    SELECT EXISTS(
        SELECT 1 FROM pg_proc
        WHERE pronamespace = 'api'::regnamespace AND proname = 'rename_beta'
          AND proargtypes[0] = 'api.rest_request'::regtype
    ) INTO v_beta_exists;

    IF v_alpha_exists IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'orphaned function api.rename_alpha was not dropped on rename';
    END IF;
    IF v_beta_exists IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'replacement function api.rename_beta should exist after rename';
    END IF;

    SELECT handler_function_name INTO v_registered FROM api.handler WHERE object_id = v_id;
    IF v_registered IS DISTINCT FROM 'rename_beta' THEN
        RAISE EXCEPTION 'registry should point at rename_beta, got %', v_registered;
    END IF;
END $$;

DO $$
BEGIN
END $$;

-- ############################################################################
-- COLLISIONS - a name, a folded name or an id is claimed by one handler only
-- Names become function names (api.<name>), so a second CREATE OR REPLACE would
-- overwrite the first handler's body. Registration must refuse and say who owns
-- the name, not fail with a raw constraint violation that names neither.
-- ############################################################################
DO $$
DECLARE
    v_first  uuid := 'ffffffff-0186-4000-8000-000000000001';
    v_second uuid := 'ffffffff-0186-4000-8000-000000000002';
    v_message text;
    v_body text;
BEGIN
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', v_first, 'path', '/collide-first', 'httpMethod', '^GET$',
            'name', 'collision_probe', 'access', 'public', 'autoLog', false),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('owner', 'first')); END; $body$
    );

    -- Same name, different handler: a realistic copy-paste of the metadata block.
    BEGIN
        PERFORM api.create_or_replace_rest_handler(
            jsonb_build_object('id', v_second, 'path', '/collide-second', 'httpMethod', '^GET$',
                'name', 'collision_probe', 'access', 'public', 'autoLog', false),
            $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('owner', 'second')); END; $body$
        );
        RAISE EXCEPTION 'TEST FAILED: two REST handlers must not both claim the name "collision_probe"';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT;
    END;

    -- The message must be actionable: it names the collision and its current owner.
    IF coalesce(v_message, '') NOT LIKE '%collision_probe%' OR coalesce(v_message, '') NOT LIKE '%' || v_first::text || '%' THEN
        RAISE EXCEPTION 'TEST FAILED: the error must name the route and the handler that owns it, got: %', v_message;
    END IF;

    -- And the incumbent still runs its OWN body — the whole point of the guard.
    v_body := convert_from((api.rest_invoke('GET', '/collide-first')).content, 'UTF8');
    IF coalesce(v_body, '') NOT LIKE '%first%' THEN
        RAISE EXCEPTION 'TEST FAILED: the incumbent handler''s body was overwritten, got: %', v_body;
    END IF;

    -- Re-registering under the SAME id must still work: that is a replacement,
    -- not a collision. A guard that blocked this would break every redeploy.
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', v_first, 'path', '/collide-first', 'httpMethod', '^GET$',
            'name', 'collision_probe', 'access', 'public', 'autoLog', false),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('owner', 'first-v2')); END; $body$
    );

    v_body := convert_from((api.rest_invoke('GET', '/collide-first')).content, 'UTF8');
    IF coalesce(v_body, '') NOT LIKE '%first-v2%' THEN
        RAISE EXCEPTION 'TEST FAILED: re-registering the same id must replace the body, got: %', v_body;
    END IF;
END $$;

DO $$
DECLARE
    v_first  uuid := 'ffffffff-0186-4000-8000-000000000003';
    v_second uuid := 'ffffffff-0186-4000-8000-000000000004';
    v_message text;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', v_first, 'type', 'tool', 'effect', 'read', 'name', 'mcp_collision_probe',
            'description', 'first', 'access', 'public'),
        $body$ BEGIN RETURN api.mcp_tool_result(to_jsonb('first'::text), (request).request_id); END; $body$
    );

    BEGIN
        PERFORM api.create_or_replace_mcp_handler(
            jsonb_build_object('id', v_second, 'type', 'tool', 'effect', 'read', 'name', 'mcp_collision_probe',
                'description', 'second', 'access', 'public'),
            $body$ BEGIN RETURN api.mcp_tool_result(to_jsonb('second'::text), (request).request_id); END; $body$
        );
        RAISE EXCEPTION 'TEST FAILED: two MCP handlers must not both claim one name';
    EXCEPTION WHEN unique_violation THEN
        GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT;
    END;

    -- A raw "duplicate key value violates unique constraint" names neither handler.
    IF coalesce(v_message, '') NOT LIKE '%mcp_collision_probe%' OR coalesce(v_message, '') NOT LIKE '%' || v_first::text || '%' THEN
        RAISE EXCEPTION 'TEST FAILED: MCP collision must report a clean, attributable error, got: %', v_message;
    END IF;
END $$;

-- RPC and MCP fold '.' and '-' to '_' when deriving the function name, so two
-- distinct names can claim one function. The raw-name guards above cannot see
-- that; unguarded, the second registration replaces the first handler's body
-- and then fails on handler_func with a message naming neither.
DO $$
DECLARE
    v_case    record;
    v_message text;
BEGIN
    PERFORM api.create_or_replace_rpc_handler(
        jsonb_build_object('id', 'ffffffff-0290-4000-8000-000000000001',
            'methodName', 'reports.run', 'access', 'public'),
        $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('owner', 'rpc-first')); END; $body$);
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0290-4000-8000-000000000003',
            'type', 'tool', 'effect', 'read', 'name', 'fold.probe', 'description', 'first', 'access', 'public'),
        $body$ BEGIN RETURN api.mcp_tool_result(to_jsonb('mcp-first'::text), (request).request_id); END; $body$);

    FOR v_case IN
        SELECT * FROM (VALUES
            ('rpc', 'reports-run', 'reports.run', 'api.rpc_reports_run(api.rpc_request)',       'rpc-first'),
            ('mcp', 'fold-probe',  'fold.probe',  'api.mcp_tool_fold_probe(api.mcp_request)',   'mcp-first')
        ) AS c(kind, second_name, first_name, fn, first_marker)
    LOOP
        v_message := NULL;
        BEGIN
            IF v_case.kind = 'rpc' THEN
                PERFORM api.create_or_replace_rpc_handler(
                    jsonb_build_object('id', 'ffffffff-0290-4000-8000-000000000002',
                        'methodName', v_case.second_name, 'access', 'public'),
                    $body$ BEGIN RETURN api.json_response(200, jsonb_build_object('owner', 'second')); END; $body$);
            ELSE
                PERFORM api.create_or_replace_mcp_handler(
                    jsonb_build_object('id', 'ffffffff-0290-4000-8000-000000000004',
                        'type', 'tool', 'effect', 'read', 'name', v_case.second_name, 'description', 'second', 'access', 'public'),
                    $body$ BEGIN RETURN api.mcp_tool_result(to_jsonb('second'::text), (request).request_id); END; $body$);
            END IF;
            RAISE EXCEPTION 'TEST FAILED: % "%" and "%" must not share one function', v_case.kind, v_case.first_name, v_case.second_name;
        EXCEPTION WHEN unique_violation THEN
            GET STACKED DIAGNOSTICS v_message = MESSAGE_TEXT;
        END;

        IF coalesce(v_message, '') NOT LIKE '%"' || v_case.first_name || '"%'
           OR coalesce(v_message, '') NOT LIKE '%"' || v_case.second_name || '"%' THEN
            RAISE EXCEPTION 'TEST FAILED: % collision must name both "%" and "%", got: %',
                v_case.kind, v_case.first_name, v_case.second_name, v_message;
        END IF;

        IF coalesce((SELECT prosrc FROM pg_proc WHERE oid = to_regprocedure(v_case.fn)), '')
           NOT LIKE '%' || v_case.first_marker || '%' THEN
            RAISE EXCEPTION 'TEST FAILED: the refused % registration replaced the body of %', v_case.kind, v_case.fn;
        END IF;
    END LOOP;
END $$;

-- An id belongs to one protocol: reusing it for another is refused and the
-- existing handler stays as it was; re-registering it under its own protocol
-- still works.
DO $$
DECLARE
    v_case record;
    v_msg  text;
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'ffffffff-0720-4000-8000-000000000001', 'type', 'tool', 'effect', 'read',
            'name', 'reuse_tool', 'description', 'A tool', 'access', 'public'),
        $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END;$b$);
    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0720-4000-8000-000000000002', 'path', '/reuse-route', 'httpMethod', '^GET$',
            'name', 'reuse_route', 'access', 'public', 'outputSchema', '{"type": "object"}'::jsonb),
        $b$BEGIN RETURN api.json_response(200, '{}'); END;$b$);

    FOR v_case IN SELECT * FROM (VALUES
        ('ffffffff-0720-4000-8000-000000000001'::uuid, 'rest', 'mcp_tool'),
        ('ffffffff-0720-4000-8000-000000000002'::uuid, 'mcp', 'rest')) c(id, new_protocol, kept_type)
    LOOP
        v_msg := NULL;
        BEGIN
            IF v_case.new_protocol = 'rest' THEN
                PERFORM api.create_or_replace_rest_handler(
                    jsonb_build_object('id', v_case.id, 'path', '/reuse-other', 'httpMethod', '^GET$',
                        'name', 'reuse_other', 'access', 'public', 'outputSchema', '{"type": "object"}'::jsonb),
                    $b$BEGIN RETURN api.json_response(200, '{}'); END;$b$);
            ELSE
                PERFORM api.create_or_replace_mcp_handler(
                    jsonb_build_object('id', v_case.id, 'type', 'tool', 'effect', 'read',
                        'name', 'reuse_other', 'description', 'Another tool', 'access', 'public'),
                    $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END;$b$);
            END IF;
        EXCEPTION WHEN invalid_parameter_value THEN
            v_msg := SQLERRM;
        END;
        IF coalesce(v_msg, '') NOT LIKE '%' || v_case.id || '%'
           OR (SELECT handler_type::text FROM api.handler WHERE object_id = v_case.id) IS DISTINCT FROM v_case.kept_type THEN
            RAISE EXCEPTION 'TEST FAILED: reusing % for % must be refused and keep %, got %', v_case.id, v_case.new_protocol, v_case.kept_type, v_msg;
        END IF;
    END LOOP;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-0720-4000-8000-000000000002', 'path', '/reuse-route', 'httpMethod', '^GET$',
            'name', 'reuse_route', 'access', 'public', 'outputSchema', '{"type": "object"}'::jsonb),
        $b$BEGIN RETURN api.json_response(200, '{"again": true}'); END;$b$);
END $$;
