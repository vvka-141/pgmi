/*
<pgmi-meta
    id="a7f01000-0007-4000-8000-000000000001"
    idempotent="true">
  <description>
    Helper functions: content parsing, URL parsing, and response builders
  </description>
  <sortKeys>
    <key>004/007</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing API helper functions'; END $$;

-- ============================================================================
-- Content Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.content_json(content bytea)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT content::common.utf8::jsonb;
$$;

COMMENT ON FUNCTION api.content_json(bytea) IS
    'Casts request body (bytea) to jsonb via UTF-8 text.';

CREATE OR REPLACE FUNCTION api.content_text(content bytea)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT content::common.utf8::text;
$$;

COMMENT ON FUNCTION api.content_text(bytea) IS
    'Casts request body (bytea) to text via UTF-8.';

CREATE OR REPLACE FUNCTION api.header(headers extensions.hstore, name text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT headers->lower(name);
$$;

COMMENT ON FUNCTION api.header(extensions.hstore, text) IS
    'Case-insensitive header lookup. Lowercases the name before hstore lookup.';

-- Inline tests
DO $$
DECLARE
    v_json jsonb;
    v_text text;
    v_header text;
    v_headers extensions.hstore;
BEGIN
    v_json := api.content_json(convert_to('{"key": "value"}', 'UTF8'));
    IF v_json->>'key' != 'value' THEN
        RAISE EXCEPTION 'content_json failed';
    END IF;

    v_text := api.content_text(convert_to('hello world', 'UTF8'));
    IF v_text != 'hello world' THEN
        RAISE EXCEPTION 'content_text failed';
    END IF;

    v_headers := extensions.hstore(ARRAY['content-type', 'application/json']);
    v_header := api.header(v_headers, 'Content-Type');
    IF v_header != 'application/json' THEN
        RAISE EXCEPTION 'header case-insensitive lookup failed';
    END IF;
END $$;

-- ============================================================================
-- URL Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.url_decode(p_encoded text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    -- Tokenise in a single pass, decode each valid %HH into a bytea, concat
    -- and interpret as UTF-8. Avoids the O(n²) string-rebuild loop the prior
    -- plpgsql implementation used, which amplified DoS surface on long
    -- percent-encoded URLs. Invalid %XX and trailing partial sequences are
    -- preserved as literals — same semantics, verified by the inline tests.
    WITH parts AS (
        SELECT m[1] AS piece, ord
        FROM regexp_matches(
            replace(p_encoded, '+', ' '),
            '(%[0-9A-Fa-f]{2}|[^%]+|%)',
            'g'
        ) WITH ORDINALITY AS t(m, ord)
    )
    SELECT convert_from(
        COALESCE(
            string_agg(
                CASE
                    WHEN piece ~ '^%[0-9A-Fa-f]{2}$'
                    THEN decode(substring(piece FROM 2), 'hex')
                    ELSE convert_to(piece, 'UTF8')
                END,
                ''::bytea ORDER BY ord
            ),
            ''::bytea
        ),
        'UTF8'
    ) FROM parts;
$$;

COMMENT ON FUNCTION api.url_decode(text) IS
    'Decodes percent-encoded URL strings. Handles %HH sequences and + as space; invalid or trailing percent sequences are preserved as literals.';

CREATE OR REPLACE FUNCTION api.regexp_quote(p_text text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT regexp_replace(p_text, '([\\.^$|?*+()[\]{}])', '\\\1', 'g');
$$;

COMMENT ON FUNCTION api.regexp_quote(text) IS
    'Escapes POSIX regex metacharacters so the input can be used as a literal in a regex pattern.';

CREATE OR REPLACE FUNCTION api.uri_template_to_regex(p_template text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT '^' || regexp_replace(
        api.regexp_quote(
            regexp_replace(p_template, '\{[^}]+\}', '<<<PLACEHOLDER>>>', 'g')
        ),
        '<<<PLACEHOLDER>>>',
        '[^/]+',
        'g'
    ) || '$';
$$;

COMMENT ON FUNCTION api.uri_template_to_regex(text) IS
    'Converts a URI template (e.g. /users/{id}) to an anchored POSIX regex where each {param} matches one path segment.';

-- ============================================================================
-- MCP Resource URI Regex — derived once, not per request
-- ============================================================================
-- URI templates are static after registration, so converting them on every
-- resources/read was pure repeated work: three regexp_replace passes per
-- candidate route per request. api.mcp_route.uri_regexp holds the conversion and
-- dispatch matches against it.
--
-- A trigger, not a write in the registration function: admin_role holds INSERT/
-- UPDATE/DELETE on api.mcp_route, so direct DML is a granted path. A derived
-- column that only the registration function maintained would go stale silently
-- under that DML — and a stale regex mis-routes resource requests, which is a
-- worse bug than the one being fixed. The trigger makes desync impossible.

CREATE OR REPLACE FUNCTION internal.sync_mcp_uri_regexp()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.uri_regexp := api.uri_template_to_regex(NEW.uri_template);
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS tr_mcp_route_uri_regexp ON api.mcp_route;
CREATE TRIGGER tr_mcp_route_uri_regexp
    BEFORE INSERT OR UPDATE OF uri_template ON api.mcp_route
    FOR EACH ROW EXECUTE FUNCTION internal.sync_mcp_uri_regexp();

-- Backfill, and self-heal if api.uri_template_to_regex itself ever changes: the
-- trigger only fires on write, so a redeploy that redefines the conversion would
-- otherwise leave every stored regex stale.
UPDATE api.mcp_route
SET uri_regexp = api.uri_template_to_regex(uri_template)
WHERE uri_regexp IS DISTINCT FROM api.uri_template_to_regex(uri_template);

-- ============================================================================
-- Route Path Tokens
-- ============================================================================
-- Splits a stored route regex into literal runs and capture groups. The OpenAPI
-- generator and the registration guard both read path parameters from here, so
-- there is exactly one definition of "what counts as a path parameter" — two
-- would drift. group_index is NULL for literal runs and 1..N, left to right,
-- for capture groups.
--
-- The three nested strips remove the anchors and the optional query-string
-- suffix routes conventionally carry ('(\?.*)?$'), which is not a parameter.

-- Dollar-quoted with a named tag: the strip patterns below contain '$$', which
-- would close an anonymous $$ body mid-string.
CREATE OR REPLACE FUNCTION api.route_path_tokens(p_address_regexp text)
RETURNS TABLE (ord bigint, token text, group_index bigint)
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $route_path_tokens$
    WITH c_pattern AS (
        SELECT regexp_replace(
                   regexp_replace(
                       regexp_replace(p_address_regexp, '^\^', ''),
                       '\(\\\\?\?\.\*\)\?\$?$', ''),
                   '\$$', '') AS pattern
    ),
    c_token AS (
        SELECT t.ord, t.m[1] AS token
        FROM c_pattern
        CROSS JOIN LATERAL regexp_matches(c_pattern.pattern, '\([^)]*\)|[^(]+', 'g')
            WITH ORDINALITY AS t(m, ord)
    )
    SELECT
        c_token.ord,
        c_token.token,
        CASE
            WHEN c_token.token LIKE '(%'
            THEN row_number() OVER (PARTITION BY c_token.token LIKE '(%' ORDER BY c_token.ord)
        END
    FROM c_token;
$route_path_tokens$;

COMMENT ON FUNCTION api.route_path_tokens(text) IS
    'Splits a route regex into literal runs and capture groups (group_index 1..N left to right). Single source of truth for path parameters, shared by the OpenAPI generator and the registration guard.';

CREATE OR REPLACE FUNCTION api.route_path_param_name(p_param_names text[], p_index bigint)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT COALESCE(NULLIF(p_param_names[p_index::int], ''), 'p' || p_index);
$$;

COMMENT ON FUNCTION api.route_path_param_name(text[], bigint) IS
    'Resolves the name of the Nth path parameter: the handler-declared name when present, otherwise positional (p1, p2, ...).';

DO $$
BEGIN
    IF (SELECT count(*) FROM api.route_path_tokens('^/orgs/([^/]+)/users/(\d+)$') WHERE group_index IS NOT NULL) != 2 THEN
        RAISE EXCEPTION 'route_path_tokens must find both capture groups in a two-parameter route';
    END IF;

    IF (SELECT count(*) FROM api.route_path_tokens('^/hello(\?.*)?$') WHERE group_index IS NOT NULL) != 0 THEN
        RAISE EXCEPTION 'route_path_tokens must not treat the query-string suffix as a path parameter';
    END IF;

    IF api.route_path_param_name(ARRAY['orgId'], 1) != 'orgId'
       OR api.route_path_param_name('{}'::text[], 2) != 'p2' THEN
        RAISE EXCEPTION 'route_path_param_name must prefer the declared name and fall back to positional';
    END IF;
END $$;

-- HTTP Accept content negotiation. Returns true when the client accepts at
-- least one of the server's produced media types. Parses the Accept header
-- into media ranges (split on ',', drop ';q='/parameters) and matches each
-- against p_produces with wildcard support: */* and type/*. Substring matching
-- is avoided so application/* matches application/json (no false 406) and
-- application/json-patch+json does NOT match application/json (no false accept).
-- Absent/empty Accept, or a route that declares no produced types, accepts all.
CREATE OR REPLACE FUNCTION api.accept_matches(p_accept text, p_produces text[])
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN p_accept IS NULL OR btrim(p_accept) = '' THEN true
        WHEN p_produces IS NULL OR cardinality(p_produces) = 0 THEN true
        ELSE EXISTS (
            SELECT 1
            FROM unnest(string_to_array(p_accept, ',')) AS raw_range
            CROSS JOIN LATERAL (
                SELECT lower(btrim(split_part(raw_range, ';', 1))) AS media_range
            ) r
            CROSS JOIN unnest(p_produces) AS produced
            WHERE r.media_range = '*/*'
               OR r.media_range = lower(btrim(produced))
               OR (right(r.media_range, 2) = '/*'
                   AND split_part(r.media_range, '/', 1) = lower(split_part(btrim(produced), '/', 1)))
        )
    END;
$$;

COMMENT ON FUNCTION api.accept_matches(text, text[]) IS
    'HTTP content negotiation. Returns true when the Accept header matches at least one of the produced media types; supports */*, type/* wildcards. NULL or empty Accept accepts all.';

-- Single-value query-string parser. hstore cannot hold duplicate keys, so for
-- a repeated key (?tag=a&tag=b) this keeps the FIRST occurrence. Use
-- api.query_params_multi() when a parameter may legitimately repeat.
CREATE OR REPLACE FUNCTION api.query_params(url text)
RETURNS extensions.hstore
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    WITH params AS (
        SELECT unnest(string_to_array(split_part(url, '?', 2), '&')) AS param
    )
    SELECT COALESCE(
        extensions.hstore(
            array_agg(api.url_decode(split_part(param, '=', 1))),
            array_agg(api.url_decode(COALESCE(nullif(split_part(param, '=', 2), ''), '')))
        ),
        ''::extensions.hstore
    )
    FROM params WHERE param != '';
$$;

COMMENT ON FUNCTION api.query_params(text) IS
    'Single-value query-string parser (hstore). Repeated keys collapse to the first value; use api.query_params_multi() to preserve all values.';

-- Multi-value query-string parser. Returns jsonb mapping each key to a JSON
-- array of all its values in order, so ?tag=a&tag=b yields {"tag": ["a","b"]}.
-- A single-value param is a one-element array the caller unwraps with ->>0.
CREATE OR REPLACE FUNCTION api.query_params_multi(url text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    WITH params AS (
        SELECT
            api.url_decode(split_part(param, '=', 1)) AS key,
            api.url_decode(COALESCE(nullif(split_part(param, '=', 2), ''), '')) AS value,
            ordinality
        FROM unnest(string_to_array(split_part(url, '?', 2), '&')) WITH ORDINALITY AS t(param, ordinality)
        WHERE param != ''
    )
    SELECT COALESCE(
        jsonb_object_agg(key, arr),
        '{}'::jsonb
    )
    FROM (
        SELECT key, jsonb_agg(value ORDER BY ordinality) AS arr
        FROM params
        GROUP BY key
    ) grouped;
$$;

COMMENT ON FUNCTION api.query_params_multi(text) IS
    'Multi-value query-string parser (jsonb). Each key maps to a JSON array of all its values in order; preserves duplicate keys that api.query_params() would drop.';

CREATE OR REPLACE FUNCTION api.url_path(url text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT split_part(url, '?', 1);
$$;

COMMENT ON FUNCTION api.url_path(text) IS
    'Extracts the path component from a URL by stripping the query string.';

-- Inline tests
DO $$
DECLARE
    v_params extensions.hstore;
    v_params_multi jsonb;
    v_path text;
    v_decoded text;
    v_quoted text;
BEGIN
    -- Test accept_matches (PGMI-31 content negotiation)
    IF NOT api.accept_matches('application/*', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: media range application/* should match application/json';
    END IF;
    IF api.accept_matches('application/json-patch+json', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: application/json-patch+json must NOT match application/json';
    END IF;
    IF NOT api.accept_matches('*/*', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: */* should match anything';
    END IF;
    IF NOT api.accept_matches(NULL, ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: absent Accept should match';
    END IF;
    IF NOT api.accept_matches('text/html, application/json;q=0.9', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: a later media range with q-param should still match';
    END IF;
    IF api.accept_matches('text/html', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: unrelated type must not match';
    END IF;

    -- Test url_decode
    v_decoded := api.url_decode('John%20Doe');
    IF v_decoded != 'John Doe' THEN
        RAISE EXCEPTION 'url_decode percent failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('hello+world');
    IF v_decoded != 'hello world' THEN
        RAISE EXCEPTION 'url_decode plus failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('100%25+complete');
    IF v_decoded != '100% complete' THEN
        RAISE EXCEPTION 'url_decode mixed failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('%GG%20hello');
    IF v_decoded != '%GG hello' THEN
        RAISE EXCEPTION 'url_decode must skip invalid %% and decode later valid sequences: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('trailing%');
    IF v_decoded != 'trailing%' THEN
        RAISE EXCEPTION 'url_decode trailing %% should stay literal: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('trailing%1');
    IF v_decoded != 'trailing%1' THEN
        RAISE EXCEPTION 'url_decode trailing %%X should stay literal: got %', v_decoded;
    END IF;

    -- Test regexp_quote
    v_quoted := api.regexp_quote('file.json');
    IF v_quoted != 'file\.json' THEN
        RAISE EXCEPTION 'regexp_quote dot failed: got %', v_quoted;
    END IF;

    v_quoted := api.regexp_quote('a+b*c?');
    IF v_quoted != 'a\+b\*c\?' THEN
        RAISE EXCEPTION 'regexp_quote metachar failed: got %', v_quoted;
    END IF;

    -- Test uri_template_to_regex
    v_quoted := api.uri_template_to_regex('/api/resource.json/{id}');
    IF v_quoted != '^/api/resource\.json/[^/]+$' THEN
        RAISE EXCEPTION 'uri_template_to_regex failed: got %', v_quoted;
    END IF;

    IF NOT '/api/resource.json/123' ~ api.uri_template_to_regex('/api/resource.json/{id}') THEN
        RAISE EXCEPTION 'uri_template_to_regex match failed';
    END IF;

    IF '/api/resourceXjson/123' ~ api.uri_template_to_regex('/api/resource.json/{id}') THEN
        RAISE EXCEPTION 'uri_template_to_regex should not match unescaped dot';
    END IF;

    -- Test query_params with URL encoding
    v_params := api.query_params('/api/users?name=john&age=30');
    IF v_params->'name' != 'john' OR v_params->'age' != '30' THEN
        RAISE EXCEPTION 'query_params basic failed';
    END IF;

    v_params := api.query_params('/search?q=hello%20world&filter=a%2Bb');
    IF v_params->'q' != 'hello world' THEN
        RAISE EXCEPTION 'query_params url decode failed: got %', v_params->'q';
    END IF;
    IF v_params->'filter' != 'a+b' THEN
        RAISE EXCEPTION 'query_params url decode plus failed: got %', v_params->'filter';
    END IF;

    -- query_params drops repeated keys (first wins); query_params_multi keeps all
    IF api.query_params('/f?tag=a&tag=b')->'tag' != 'a' THEN
        RAISE EXCEPTION 'query_params should keep first value for repeated key';
    END IF;

    v_params_multi := api.query_params_multi('/f?tag=a&tag=b&q=hi');
    IF v_params_multi->'tag' != '["a", "b"]'::jsonb THEN
        RAISE EXCEPTION 'query_params_multi must preserve duplicate keys: got %', v_params_multi->'tag';
    END IF;
    IF v_params_multi->'q'->>0 != 'hi' THEN
        RAISE EXCEPTION 'query_params_multi single value should be a one-element array: got %', v_params_multi->'q';
    END IF;

    v_path := api.url_path('/api/users?name=john');
    IF v_path != '/api/users' THEN
        RAISE EXCEPTION 'url_path failed';
    END IF;
END $$;

-- ============================================================================
-- Response Builders
-- ============================================================================

CREATE OR REPLACE FUNCTION api.json_response(
    status_code integer,
    content jsonb
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (
        status_code,
        extensions.hstore('content-type', 'application/json'),
        convert_to(content::text, 'UTF8')
    )::api.http_response;
$$;

COMMENT ON FUNCTION api.json_response(integer, jsonb) IS
    'Builds an HTTP response with application/json content-type and the given status code.';

-- Drop the pre-RFC-9457 signature so the extended one below is the only
-- problem_response; an added-parameter overload would make 3-arg calls
-- ambiguous. CREATE OR REPLACE cannot change the parameter count.
DROP FUNCTION IF EXISTS api.problem_response(integer, text, text, text, text);

CREATE OR REPLACE FUNCTION api.problem_response(
    status_code integer,
    title text,
    detail text DEFAULT NULL,
    type_uri text DEFAULT NULL,
    instance text DEFAULT NULL,
    code text DEFAULT NULL,
    invalid_params jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (
        status_code,
        extensions.hstore('content-type', 'application/problem+json'),
        convert_to(
            jsonb_strip_nulls(jsonb_build_object(
                'type', COALESCE(type_uri, 'about:blank'),
                'title', title,
                'status', status_code,
                'detail', detail,
                'instance', instance,
                'code', code,
                'invalid-params', invalid_params
            ))::text,
            'UTF8'
        )
    )::api.http_response;
$$;

COMMENT ON FUNCTION api.problem_response(integer, text, text, text, text, text, jsonb) IS
    'RFC 9457 problem+json response. Optional code is a stable machine-readable token; invalid_params is a jsonb array of {name,reason} (build with api.invalid_param). NULL members are stripped.';

-- RFC 9457 invalid-params member constructor: one {name, reason} entry.
CREATE OR REPLACE FUNCTION api.invalid_param(p_name text, p_reason text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object('name', p_name, 'reason', p_reason);
$$;

COMMENT ON FUNCTION api.invalid_param(text, text) IS
    'Builds one RFC 9457 invalid-params entry {name, reason}; aggregate several into a jsonb array for api.problem_response(..., invalid_params => ...).';

CREATE OR REPLACE FUNCTION api.error_response(
    status_code integer,
    message text
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.problem_response(status_code, message);
$$;

COMMENT ON FUNCTION api.error_response(integer, text) IS
    'Convenience wrapper around api.problem_response for simple error messages with no detail or type URI.';

-- Inline tests
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.json_response(200, '{"status": "ok"}'::jsonb);
    IF (v_response).status_code != 200 THEN
        RAISE EXCEPTION 'json_response status failed';
    END IF;

    v_response := api.problem_response(404, 'Not Found', 'Resource does not exist');
    v_content := api.content_json((v_response).content);
    IF v_content->>'title' != 'Not Found' THEN
        RAISE EXCEPTION 'problem_response failed';
    END IF;
    IF v_content ? 'code' OR v_content ? 'invalid-params' THEN
        RAISE EXCEPTION 'problem_response should strip absent code/invalid-params members';
    END IF;

    -- RFC 9457 fields surface when provided
    v_response := api.problem_response(
        422, 'Unprocessable', 'Validation failed',
        code => 'validation_error',
        invalid_params => jsonb_build_array(api.invalid_param('total', 'must be positive'))
    );
    v_content := api.content_json((v_response).content);
    IF v_content->>'code' != 'validation_error' THEN
        RAISE EXCEPTION 'problem_response code member missing';
    END IF;
    IF v_content->'invalid-params'->0->>'name' != 'total' THEN
        RAISE EXCEPTION 'problem_response invalid-params member missing';
    END IF;
END $$;

-- ============================================================================
-- List Pagination
-- ============================================================================
-- Uniform ?limit/?offset parsing for list handlers: clamps limit to [0, max]
-- and offset to >= 0, and returns a ready 422 in o_error when either value is
-- present but not an integer. Callers check `(o_error).status_code IS NOT NULL`
-- and return o_error before running the query. Fetch o_limit + 1 rows to derive
-- a boolean hasMore without a second count query.

CREATE OR REPLACE FUNCTION api.pagination_params(
    p_q       extensions.hstore,
    p_default integer DEFAULT 50,
    p_max     integer DEFAULT 200,
    OUT o_limit  integer,
    OUT o_offset integer,
    OUT o_error  api.http_response
)
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT
        LEAST(GREATEST(COALESCE(common.try_cast(p_q->'limit', NULL::integer), p_default), 0), p_max),
        GREATEST(COALESCE(common.try_cast(p_q->'offset', NULL::integer), 0), 0),
        CASE
            WHEN NULLIF(p_q->'limit', '') IS NOT NULL
                 AND common.try_cast(p_q->'limit', NULL::integer) IS NULL
                THEN api.problem_response(422, 'Unprocessable', 'limit must be an integer',
                        invalid_params => jsonb_build_array(api.invalid_param('limit', 'must be an integer')))
            WHEN NULLIF(p_q->'offset', '') IS NOT NULL
                 AND common.try_cast(p_q->'offset', NULL::integer) IS NULL
                THEN api.problem_response(422, 'Unprocessable', 'offset must be an integer',
                        invalid_params => jsonb_build_array(api.invalid_param('offset', 'must be an integer')))
            ELSE NULL
        END;
$$;

COMMENT ON FUNCTION api.pagination_params(extensions.hstore, integer, integer) IS
    'Parses ?limit/?offset from a query hstore: clamps limit to [0,p_max] (default p_default) and offset to >=0; sets o_error to a 422 problem_response when either is present but non-integer. Callers return o_error when its status_code is not null.';

-- Inline tests
DO $$
DECLARE
    v_page record;
BEGIN
    -- Defaults when nothing supplied
    v_page := api.pagination_params(''::extensions.hstore);
    IF v_page.o_limit <> 50 OR v_page.o_offset <> 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params defaults failed (limit=%, offset=%)', v_page.o_limit, v_page.o_offset;
    END IF;

    -- Clamp to max and floor offset at 0
    v_page := api.pagination_params('limit=>5000, offset=>-3'::extensions.hstore);
    IF v_page.o_limit <> 200 OR v_page.o_offset <> 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params clamping failed (limit=%, offset=%)', v_page.o_limit, v_page.o_offset;
    END IF;

    -- Non-integer limit -> 422 in o_error
    v_page := api.pagination_params('limit=>abc'::extensions.hstore);
    IF (v_page.o_error).status_code <> 422 THEN
        RAISE EXCEPTION 'pagination_params should 422 on non-integer limit, got %', (v_page.o_error).status_code;
    END IF;

    -- limit=0 is honored (empty page), not treated as missing
    v_page := api.pagination_params('limit=>0'::extensions.hstore);
    IF v_page.o_limit <> 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params should honor limit=0, got %', v_page.o_limit;
    END IF;
END $$;

-- ============================================================================
-- JSON-RPC Response Builders
-- ============================================================================

CREATE OR REPLACE FUNCTION api.jsonrpc_success(
    result jsonb,
    id jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.json_response(200, jsonb_build_object(
        'jsonrpc', '2.0',
        'result', result,
        'id', id
    ));
$$;

COMMENT ON FUNCTION api.jsonrpc_success(jsonb, jsonb) IS
    'JSON-RPC 2.0 success response. Returns HTTP 200 with {jsonrpc, result, id}.';

CREATE OR REPLACE FUNCTION api.jsonrpc_error(
    code integer,
    message text,
    id jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.json_response(
        CASE code
            WHEN -32700 THEN 400
            WHEN -32600 THEN 400
            WHEN -32601 THEN 404
            WHEN -32602 THEN 400
            WHEN -32603 THEN 500
            WHEN -32001 THEN 401
            ELSE 500
        END,
        jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', code, 'message', message),
            'id', id
        )
    );
$$;

COMMENT ON FUNCTION api.jsonrpc_error(integer, text, jsonb) IS
    'JSON-RPC 2.0 error response. Maps standard error codes (-32700..-32603, -32001) to HTTP status codes.';

-- Inline tests
DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.jsonrpc_success('{"value": 42}'::jsonb, '"req-1"'::jsonb);
    v_content := api.content_json((v_response).content);
    IF v_content->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'jsonrpc_success failed';
    END IF;

    v_response := api.jsonrpc_error(-32601, 'Method not found');
    IF (v_response).status_code != 404 THEN
        RAISE EXCEPTION 'jsonrpc_error status mapping failed: expected 404, got %', (v_response).status_code;
    END IF;
END $$;

-- ============================================================================
-- MCP Response Builders (JSON-RPC 2.0 Compliant)
-- ============================================================================
-- request_id is jsonb so JSON-RPC id types (string, integer, null) round-trip
-- exactly. Drop any prior text-signature variants before recreating.

DROP FUNCTION IF EXISTS api.mcp_success(jsonb, text);
DROP FUNCTION IF EXISTS api.mcp_error(integer, text, text, jsonb);
DROP FUNCTION IF EXISTS api.mcp_tool_result(jsonb, text, boolean);
DROP FUNCTION IF EXISTS api.mcp_tool_error(text, text);
DROP FUNCTION IF EXISTS api.mcp_resource_result(jsonb, text);
DROP FUNCTION IF EXISTS api.mcp_resource_error(text, text);
DROP FUNCTION IF EXISTS api.mcp_prompt_result(jsonb, text);
DROP FUNCTION IF EXISTS api.mcp_prompt_error(text, text);

CREATE OR REPLACE FUNCTION api.mcp_success(
    result jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ROW(
        jsonb_build_object(
            'jsonrpc', '2.0',
            'id', request_id,
            'result', result
        )
    )::api.mcp_response;
$$;

COMMENT ON FUNCTION api.mcp_success(jsonb, jsonb) IS
    'MCP JSON-RPC 2.0 success envelope. request_id is jsonb to preserve the original JSON type (string, integer, or null).';

CREATE OR REPLACE FUNCTION api.mcp_error(
    code integer,
    message text,
    request_id jsonb,
    data jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT ROW(
        jsonb_build_object(
            'jsonrpc', '2.0',
            'id', request_id,
            'error', jsonb_strip_nulls(jsonb_build_object(
                'code', code,
                'message', message,
                'data', data
            ))
        )
    )::api.mcp_response;
$$;

COMMENT ON FUNCTION api.mcp_error(integer, text, jsonb, jsonb) IS
    'MCP JSON-RPC 2.0 error envelope. Use for protocol-level errors (parse, method-not-found); tool execution failures use api.mcp_tool_error instead.';

CREATE OR REPLACE FUNCTION api.mcp_tool_result(
    content jsonb,
    request_id jsonb,
    is_error boolean DEFAULT false,
    structured_content jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_strip_nulls(jsonb_build_object(
            'content', content,
            'isError', is_error,
            'structuredContent', structured_content
        )),
        request_id
    );
$$;

COMMENT ON FUNCTION api.mcp_tool_result(jsonb, jsonb, boolean, jsonb) IS
    'MCP tool invocation result. is_error signals application-level failure (still uses the success envelope per MCP spec). structured_content carries typed output when the tool declares an outputSchema.';

CREATE OR REPLACE FUNCTION api.mcp_tool_error(
    message text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_tool_result(
        jsonb_build_array(jsonb_build_object('type', 'text', 'text', message)),
        request_id,
        true
    );
$$;

COMMENT ON FUNCTION api.mcp_tool_error(text, jsonb) IS
    'Convenience wrapper: builds an MCP tool result with isError=true and the message as a text content item.';

CREATE OR REPLACE FUNCTION api.mcp_resource_result(
    contents jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('contents', contents),
        request_id
    );
$$;

COMMENT ON FUNCTION api.mcp_resource_result(jsonb, jsonb) IS
    'MCP resources/read success response. contents is the JSON array of resource content items.';

CREATE OR REPLACE FUNCTION api.mcp_resource_error(
    message text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

COMMENT ON FUNCTION api.mcp_resource_error(text, jsonb) IS
    'MCP resource error. Wraps the message as a -32603 internal error in the JSON-RPC error envelope.';

CREATE OR REPLACE FUNCTION api.mcp_prompt_result(
    messages jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('messages', messages),
        request_id
    );
$$;

COMMENT ON FUNCTION api.mcp_prompt_result(jsonb, jsonb) IS
    'MCP prompts/get success response. messages is the JSON array of prompt messages.';

CREATE OR REPLACE FUNCTION api.mcp_prompt_error(
    message text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

COMMENT ON FUNCTION api.mcp_prompt_error(text, jsonb) IS
    'MCP prompt error. Wraps the message as a -32603 internal error in the JSON-RPC error envelope.';

CREATE OR REPLACE FUNCTION api.mcp_text(content text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object('type', 'text', 'text', content);
$$;

COMMENT ON FUNCTION api.mcp_text(text) IS
    'Builds an MCP text content item {type: "text", text: ...} for use in tool results and prompt messages.';

-- Inline tests for MCP response builders
DO $$
DECLARE
    v_response api.mcp_response;
    v_envelope jsonb;
    v_text jsonb;
BEGIN
    -- Test mcp_success (string id)
    v_response := api.mcp_success('{"value": 42}'::jsonb, '"req-1"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_success: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' != 'req-1' OR jsonb_typeof(v_envelope->'id') != 'string' THEN
        RAISE EXCEPTION 'mcp_success: id type not preserved as string';
    END IF;
    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'mcp_success: missing result';
    END IF;

    -- Test mcp_success with integer id — JSON-RPC 2.0 requires type preservation
    v_response := api.mcp_success('{}'::jsonb, '42'::jsonb);
    v_envelope := (v_response).envelope;
    IF jsonb_typeof(v_envelope->'id') != 'number' OR (v_envelope->>'id')::int != 42 THEN
        RAISE EXCEPTION 'mcp_success: integer id must stay numeric, got %', v_envelope->'id';
    END IF;

    -- Test mcp_error
    v_response := api.mcp_error(-32603, 'Internal error', '"req-2"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_error: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' != 'req-2' THEN
        RAISE EXCEPTION 'mcp_error: wrong id';
    END IF;
    IF (v_envelope->'error'->>'code')::int != -32603 THEN
        RAISE EXCEPTION 'mcp_error: wrong error code';
    END IF;

    -- Test mcp_tool_result
    v_response := api.mcp_tool_result('[{"type": "text", "text": "Hello"}]'::jsonb, '"req-3"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' != '2.0' THEN
        RAISE EXCEPTION 'mcp_tool_result: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->'result'->'content' IS NULL THEN
        RAISE EXCEPTION 'mcp_tool_result: missing content in result';
    END IF;

    -- Test mcp_tool_result with structured content (MCP 2025-06-18+ outputSchema pattern)
    v_response := api.mcp_tool_result(
        jsonb_build_array(jsonb_build_object('type', 'text', 'text', '{"answer":42}')),
        '"req-3b"'::jsonb,
        false,
        '{"answer":42}'::jsonb
    );
    v_envelope := (v_response).envelope;
    IF v_envelope->'result'->'structuredContent' IS NULL
       OR (v_envelope->'result'->'structuredContent'->>'answer')::int != 42 THEN
        RAISE EXCEPTION 'mcp_tool_result: structuredContent not emitted when provided';
    END IF;

    -- Test mcp_tool_error: MCP spec requires tool execution failures to use
    -- result.isError=true with content array, NOT the JSON-RPC error envelope.
    v_response := api.mcp_tool_error('Tool failed', '"req-4"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->'error' IS NOT NULL THEN
        RAISE EXCEPTION 'mcp_tool_error: must NOT use JSON-RPC error channel for tool failures';
    END IF;
    IF (v_envelope->'result'->>'isError')::boolean IS NOT TRUE THEN
        RAISE EXCEPTION 'mcp_tool_error: result.isError must be true';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM jsonb_array_elements(v_envelope->'result'->'content') AS c
        WHERE c->>'type' = 'text' AND c->>'text' = 'Tool failed'
    ) THEN
        RAISE EXCEPTION 'mcp_tool_error: result.content must contain the error message';
    END IF;

    -- Test mcp_text helper
    v_text := api.mcp_text('Hello world');
    IF v_text->>'type' != 'text' OR v_text->>'text' != 'Hello world' THEN
        RAISE EXCEPTION 'mcp_text failed';
    END IF;
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.content_json/text - bytea content parsing';
    RAISE NOTICE '  ✓ api.header - case-insensitive header lookup';
    RAISE NOTICE '  ✓ api.url_decode - percent-encoding decoder';
    RAISE NOTICE '  ✓ api.regexp_quote - regex metacharacter escaping';
    RAISE NOTICE '  ✓ api.uri_template_to_regex - URI template to regex conversion';
    RAISE NOTICE '  ✓ api.query_params/url_path - URL parsing with decoding';
    RAISE NOTICE '  ✓ api.json_response - JSON response builder';
    RAISE NOTICE '  ✓ api.problem_response - RFC 9457 error response';
    RAISE NOTICE '  ✓ api.jsonrpc_success/error - JSON-RPC 2.0 responses';
    RAISE NOTICE '  ✓ api.mcp_success/error - MCP JSON-RPC 2.0 base builders';
    RAISE NOTICE '  ✓ api.mcp_tool_result/error - MCP tool response builders';
    RAISE NOTICE '  ✓ api.mcp_resource_result/error - MCP resource response builders';
    RAISE NOTICE '  ✓ api.mcp_prompt_result/error - MCP prompt response builders';
END $$;

-- ============================================================================
-- Grant Permissions
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_admin_role);
    EXECUTE format('GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA api TO %I', v_api_role);
END $$;
