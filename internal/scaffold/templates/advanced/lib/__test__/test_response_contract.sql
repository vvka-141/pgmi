-- ============================================================================
-- Test: every 409 and 422 carries a code; every page route keeps the page
-- contract
-- ============================================================================
-- A client acts on a conflict or a refused value by its code or by the field
-- named in invalid-params; an uncoded 409 or a bare 422 leaves it the title
-- text. Every problem_response(409|422, ...) call in a routine of an
-- application schema must pass code => or invalid_params =>.
--
-- A route whose outputSchema is api.page_output_schema's envelope must be a
-- GET, declare limit and offset as integers >= 0 in its querySchema (the
-- gateway then refuses a bad one), and answer through api.page_response.
-- ============================================================================

-- The problem_response(409|422, ...) calls of a body without code => or
-- invalid_params =>, each walked to its closing parenthesis (quoted text
-- skipped), comments stripped.
CREATE FUNCTION pg_temp.uncoded_problem_calls(p_src text)
RETURNS SETOF text
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    v_src   text := regexp_replace(regexp_replace(p_src, '/\*.*?\*/', '', 'g'), '--[^\n]*', '', 'g');
    v_start int;
    v_pos   int;
    v_depth int;
    v_quote boolean;
    v_ch    text;
    v_call  text;
BEGIN
    LOOP
        v_start := (SELECT position(m[1] IN v_src)
                    FROM regexp_match(v_src, '(\mproblem_response\s*\(\s*(409|422)\M)') m);
        EXIT WHEN v_start IS NULL OR v_start = 0;
        v_pos := v_start + strpos(substr(v_src, v_start), '(') - 1;
        v_depth := 0;
        v_quote := false;
        LOOP
            v_ch := substr(v_src, v_pos, 1);
            EXIT WHEN v_ch = '';
            IF v_ch = '''' THEN
                v_quote := NOT v_quote;
            ELSIF NOT v_quote AND v_ch = '(' THEN
                v_depth := v_depth + 1;
            ELSIF NOT v_quote AND v_ch = ')' THEN
                v_depth := v_depth - 1;
                EXIT WHEN v_depth = 0;
            END IF;
            v_pos := v_pos + 1;
        END LOOP;
        v_call := substr(v_src, v_start, v_pos - v_start + 1);
        IF v_call !~ '\m(code|invalid_params)\s*=>' THEN
            RETURN NEXT regexp_replace(v_call, '\s+', ' ', 'g');
        END IF;
        v_src := substr(v_src, v_pos + 1);
    END LOOP;
END;
$$;

CREATE FUNCTION pg_temp.uncoded_problems()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT p.oid::regprocedure::text || ': ' || c
    FROM pg_proc p
    JOIN pg_temp.application_schema() s ON s.oid = p.pronamespace
    CROSS JOIN LATERAL pg_temp.uncoded_problem_calls(p.prosrc) c
    WHERE p.prolang IN (SELECT oid FROM pg_language WHERE lanname IN ('sql', 'plpgsql'));
$$;

CREATE FUNCTION pg_temp.page_contract_violations()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH page_route AS (
        SELECT h.handler_function_name AS name, h.handler_exec_sql, r.method_regexp, r.query_schema,
               (SELECT p.prosrc FROM pg_proc p
                WHERE p.pronamespace = 'api'::regnamespace AND p.proname = h.handler_function_name) AS src
        FROM api.handler h
        JOIN api.rest_route r ON r.handler_object_id = h.object_id
        WHERE h.deleted_at IS NULL
          AND h.output_json_schema::jsonb->'required' @> '["items", "total", "limit", "offset", "hasMore"]'
    )
    SELECT name || ': not a GET' FROM page_route WHERE method_regexp IS DISTINCT FROM '^GET$'
    UNION ALL
    SELECT name || ': querySchema does not declare ' || k || ' as an integer >= 0'
    FROM page_route, unnest(ARRAY['limit', 'offset']) k
    WHERE query_schema->'properties'->k->>'type' IS DISTINCT FROM 'integer'
       OR (query_schema->'properties'->k->>'minimum')::numeric IS DISTINCT FROM 0
    UNION ALL
    SELECT name || ': does not answer through api.page_response' FROM page_route
    WHERE coalesce(src, handler_exec_sql, '') !~ '\mpage_response\s*\(';
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    RAISE NOTICE '-> Every 409 and 422 carries a code';

    IF (SELECT count(*) FROM pg_temp.uncoded_problem_calls($b$
            RETURN api.problem_response(409, 'Conflict', format('(%s) moved', x));
            RETURN api.problem_response(422, 'Unprocessable Entity', 'bad (value)');
            RETURN api.problem_response(409, 'Conflict', 'it moved (again)',
                                        code => 'task.moved');
            RETURN api.problem_response(422, 'Unprocessable Entity', 'x',
                                        invalid_params => jsonb_build_array(api.invalid_param('x', 'invalid')));
            -- RETURN api.problem_response(409, 'commented out');
            RETURN api.problem_response(404, 'Not Found');
        $b$)) IS DISTINCT FROM 2::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: the scan must find exactly the uncoded 409 and the bare 422 of the sample';
    END IF;

    SELECT string_agg(c, E'\n  ' ORDER BY c) INTO v_bad FROM pg_temp.uncoded_problems() c;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: 409/422 answers without code or invalid_params:\n  %', v_bad;
    END IF;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e002-4000-8000-000000000001', 'path', '/uncoded-probe',
                           'httpMethod', '^POST$', 'name', 'uncoded_probe', 'access', 'public'),
        $body$
BEGIN
    RETURN api.problem_response(409, 'Conflict', 'The task moved');
END;
$body$);
    SELECT string_agg(c, ', ') INTO v_bad FROM pg_temp.uncoded_problems() c;
    IF coalesce(v_bad, '') NOT LIKE '%uncoded_probe%' THEN
        RAISE EXCEPTION 'TEST FAILED: a planted handler answering an uncoded 409 must fail the guard, named: %', v_bad;
    END IF;
END $$;

DO $$
DECLARE
    v_bad      text;
    v_operator uuid;
    v_headers  extensions.hstore;
    v_response api.http_response;
    v_body     jsonb;
BEGIN
    RAISE NOTICE '-> Every page route keeps the page contract';

    IF NOT EXISTS (SELECT FROM api.handler h WHERE h.deleted_at IS NULL
                   AND h.output_json_schema::jsonb->'required' @> '["items", "total", "limit", "offset", "hasMore"]') THEN
        RAISE EXCEPTION 'TEST FAILED: no route answers a page, so the page contract checked nothing';
    END IF;
    SELECT string_agg(c, E'\n  ' ORDER BY c) INTO v_bad FROM pg_temp.page_contract_violations() c;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: page routes break the page contract:\n  %', v_bad;
    END IF;

    v_operator := pg_temp.test_login('test', 'page-operator', 'page-operator@example.test', NULL, true);
    PERFORM pg_temp.test_operator(v_operator);
    v_headers := pg_temp.test_session_headers('test|page-operator', NULL, ARRAY['mfa']);
    PERFORM api.rest_invoke('GET', '/admin/handlers', v_headers, NULL::bytea);
    v_response := api.rest_invoke('GET', '/admin/exchanges?limit=1', v_headers, NULL::bytea);
    v_body := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 200 OR jsonb_array_length(v_body->'items') IS DISTINCT FROM 1
       OR (v_body->>'limit')::int IS DISTINCT FROM 1 OR (v_body->>'offset')::int IS DISTINCT FROM 0
       OR (v_body->>'hasMore')::boolean IS DISTINCT FROM ((v_body->>'total')::int > 1) THEN
        RAISE EXCEPTION 'TEST FAILED: GET /admin/exchanges?limit=1 must answer one item of the page envelope, got % %',
            (v_response).status_code, v_body;
    END IF;
    v_response := api.rest_invoke('GET', '/admin/exchanges?offset=-1', v_headers, NULL::bytea);
    IF (v_response).status_code IS DISTINCT FROM 400
       OR api.content_json((v_response).content)->>'code' IS DISTINCT FROM 'pgmi.invalid_query' THEN
        RAISE EXCEPTION 'TEST FAILED: a negative offset must be the gateway''s 400 pgmi.invalid_query, got %', (v_response).status_code;
    END IF;

    PERFORM api.create_or_replace_rest_handler(
        jsonb_build_object('id', 'ffffffff-e002-4000-8000-000000000002', 'path', '/page-probe',
                           'httpMethod', '^GET$', 'name', 'page_probe', 'access', 'public',
                           'outputSchema', api.page_output_schema()),
        $body$
BEGIN
    RETURN api.json_response(200, jsonb_build_object('items', '[]'::jsonb));
END;
$body$);
    SELECT string_agg(c, ', ' ORDER BY c) INTO v_bad FROM pg_temp.page_contract_violations() c;
    IF coalesce(v_bad, '') NOT LIKE '%page_probe: querySchema does not declare limit%'
       OR coalesce(v_bad, '') NOT LIKE '%page_probe: does not answer through api.page_response%' THEN
        RAISE EXCEPTION 'TEST FAILED: a planted page route without limit or page_response must fail the guard, named: %', v_bad;
    END IF;
END $$;
