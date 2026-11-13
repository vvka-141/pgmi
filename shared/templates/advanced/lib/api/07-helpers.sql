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

DO $$
DECLARE
    v_json jsonb;
    v_text text;
    v_header text;
    v_headers extensions.hstore;
BEGIN
    v_json := api.content_json(convert_to('{"key": "value"}', 'UTF8'));
    IF v_json->>'key' IS DISTINCT FROM 'value' THEN
        RAISE EXCEPTION 'content_json failed';
    END IF;

    v_text := api.content_text(convert_to('hello world', 'UTF8'));
    IF v_text IS DISTINCT FROM 'hello world' THEN
        RAISE EXCEPTION 'content_text failed';
    END IF;

    v_headers := extensions.hstore(ARRAY['content-type', 'application/json']);
    v_header := api.header(v_headers, 'Content-Type');
    IF v_header IS DISTINCT FROM 'application/json' THEN
        RAISE EXCEPTION 'header case-insensitive lookup failed';
    END IF;
END $$;

-- ============================================================================
-- URL Parsing
-- ============================================================================

CREATE OR REPLACE FUNCTION api.url_decode(p_encoded text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    -- One tokenising pass, each valid %HH decoded into bytea, then read as
    -- UTF-8: linear in the URL's length, where a per-escape string rebuild is
    -- O(n²) on long percent-encoded URLs. Invalid %XX and a trailing partial
    -- sequence stay literal.
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

-- The one conversion behind api.uri_template_to_regex (MCP resources) and
-- api.path_template_to_regex (REST routes, which capture each parameter).
CREATE OR REPLACE FUNCTION internal.template_to_regex(p_template text, p_parameter_regex text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT '^' || regexp_replace(
        api.regexp_quote(
            regexp_replace(p_template, '\{[^}]+\}', '<<<PLACEHOLDER>>>', 'g')
        ),
        '<<<PLACEHOLDER>>>',
        p_parameter_regex,
        'g'
    ) || '$';
$$;

CREATE OR REPLACE FUNCTION api.uri_template_to_regex(p_template text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT internal.template_to_regex(p_template, '[^/]+');
$$;

COMMENT ON FUNCTION api.uri_template_to_regex(text) IS
    'Converts a URI template (e.g. /users/{id}) to an anchored POSIX regex where each {param} matches one path segment.';

-- ============================================================================
-- MCP Resource URI Regex — derived once, not per request
-- ============================================================================
-- URI templates are static after registration, so api.mcp_route.uri_regexp
-- holds the conversion and dispatch matches against it instead of converting
-- every candidate route on every resources/read.
--
-- A trigger, not a write in the registration function: admin_role holds INSERT/
-- UPDATE/DELETE on api.mcp_route, and a regex that went stale under that DML
-- would mis-route resource requests.

CREATE OR REPLACE FUNCTION internal.sync_mcp_uri_regexp()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.uri_regexp := api.uri_template_to_regex(NEW.uri_template);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE TRIGGER tr_mcp_route_uri_regexp
    BEFORE INSERT OR UPDATE OF uri_template ON api.mcp_route
    FOR EACH ROW EXECUTE FUNCTION internal.sync_mcp_uri_regexp();

-- Backfill, and self-heal if api.uri_template_to_regex itself ever changes: the
-- trigger only fires on write, so a redeploy that redefines the conversion would
-- otherwise leave every stored regex stale.
UPDATE api.mcp_route
SET uri_regexp = api.uri_template_to_regex(uri_template)
WHERE uri_regexp IS DISTINCT FROM api.uri_template_to_regex(uri_template);

-- ============================================================================
-- Path Template Helpers
-- ============================================================================

CREATE OR REPLACE FUNCTION api.path_template_to_regex(p_path text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT internal.template_to_regex(p_path, '([^/]+)');
$$;

COMMENT ON FUNCTION api.path_template_to_regex(text) IS
    'Converts a path template (e.g. /users/{id}) to an anchored POSIX regex with capture groups for each parameter.';

-- A template parameter is {name} (text), {name:uuid} or {name:int}.
CREATE OR REPLACE FUNCTION api.path_template_canonical(p_path text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT regexp_replace(p_path, '\{([^}:]+):[^}]*\}', '{\1}', 'g');
$$;

COMMENT ON FUNCTION api.path_template_canonical(text) IS
    'The template with parameter types removed: /projects/{id:uuid} returns /projects/{id}. This is the OpenAPI path.';

CREATE OR REPLACE FUNCTION api.path_template_param_spec(p_path text)
RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE
    v_spec jsonb;
    v_bad  text;
BEGIN
    SELECT COALESCE(jsonb_agg(jsonb_build_object('name', m[1], 'type', COALESCE(m[2], 'text')) ORDER BY ord), '[]'::jsonb),
           string_agg(m[2], ', ') FILTER (WHERE m[2] NOT IN ('uuid', 'int', 'text'))
      INTO v_spec, v_bad
      FROM regexp_matches(p_path, '\{([^}:]+)(?::([^}]*))?\}', 'g') WITH ORDINALITY AS t(m, ord);
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION 'unknown path parameter type "%" in %', v_bad, p_path
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Use {name}, {name:uuid} or {name:int}.';
    END IF;
    RETURN v_spec;
END $$;

COMMENT ON FUNCTION api.path_template_param_spec(text) IS
    'The template''s parameters in capture order with their types: /projects/{id:uuid}/tasks/{n} returns [{"name": "id", "type": "uuid"}, {"name": "n", "type": "text"}]. Refuses an unknown type.';

DO $$
BEGIN
    IF api.path_template_to_regex('/users/{id}') IS DISTINCT FROM '^/users/([^/]+)$'
       OR api.path_template_to_regex('/users/{id:uuid}') IS DISTINCT FROM '^/users/([^/]+)$'
       OR api.path_template_to_regex('/openapi.json') IS DISTINCT FROM '^/openapi\.json$'
       OR api.path_template_to_regex('/orgs/{orgId}/users/{userId}') IS DISTINCT FROM '^/orgs/([^/]+)/users/([^/]+)$'
       OR api.path_template_to_regex('/hello') IS DISTINCT FROM '^/hello$'
       OR api.path_template_canonical('/users/{id:uuid}/x/{n}') IS DISTINCT FROM '/users/{id}/x/{n}'
       OR api.path_template_param_spec('/users/{id:uuid}/x/{n}')
          IS DISTINCT FROM '[{"name": "id", "type": "uuid"}, {"name": "n", "type": "text"}]'::jsonb
       OR api.path_template_param_spec('/hello') IS DISTINCT FROM '[]'::jsonb THEN
        RAISE EXCEPTION 'path template helpers disagree with their examples';
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
            SELECT FROM unnest(p_produces) AS produced
            CROSS JOIN LATERAL (
                -- RFC 9110 §12.5.1: the most specific range that matches a
                -- type decides its q, so application/json;q=0 refuses JSON
                -- even next to */*.
                SELECT r.qvalue
                FROM unnest(string_to_array(p_accept, ',')) AS raw_range
                CROSS JOIN LATERAL (
                    SELECT lower(btrim(split_part(raw_range, ';', 1))) AS media_range,
                           -- A q outside the RFC 9110 12.4.2 grammar is ignored
                           -- (read as 1): casting it would raise 22P02 outside any
                           -- handler, and the request would get no HTTP response.
                           COALESCE((
                               SELECT q::numeric
                               FROM (SELECT substring(lower(raw_range) FROM ';[[:space:]]*q=([^;[:space:]]*)') AS q) qv
                               WHERE q ~ '^(0(\.[0-9]{0,3})?|1(\.0{0,3})?)$'
                           ), 1) AS qvalue
                ) r
                CROSS JOIN LATERAL (
                    SELECT CASE
                        WHEN r.media_range = lower(btrim(produced)) THEN 3
                        WHEN right(r.media_range, 2) = '/*'
                             AND split_part(r.media_range, '/', 1) = lower(split_part(btrim(produced), '/', 1))
                             AND r.media_range <> '*/*' THEN 2
                        WHEN r.media_range = '*/*' THEN 1
                    END AS specificity
                ) m
                WHERE m.specificity IS NOT NULL
                ORDER BY m.specificity DESC, r.qvalue DESC
                LIMIT 1
            ) decided
            WHERE decided.qvalue > 0
        )
    END;
$$;

COMMENT ON FUNCTION api.accept_matches(text, text[]) IS
    'HTTP content negotiation. Returns true when the Accept header matches at least one of the produced media types; supports */*, type/* wildcards; the most specific matching range decides each type''s q, and q=0 refuses it (RFC 9110 12.5.1). NULL or empty Accept accepts all.';

-- The query string's key/value pairs in order, decoded. The query is everything
-- after the first '?', and a value is everything after its first '=', so '?'
-- and '=' inside a value survive (?t=YWJj== keeps YWJj==).
CREATE OR REPLACE FUNCTION api.query_pairs(url text)
RETURNS TABLE (key text, value text, ord bigint)
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT api.url_decode(CASE WHEN strpos(t.pair, '=') > 0 THEN left(t.pair, strpos(t.pair, '=') - 1) ELSE t.pair END),
           api.url_decode(CASE WHEN strpos(t.pair, '=') > 0 THEN substr(t.pair, strpos(t.pair, '=') + 1) ELSE '' END),
           t.ord
    FROM unnest(string_to_array(
             CASE WHEN strpos(url, '?') > 0 THEN split_part(substr(url, strpos(url, '?') + 1), '#', 1) ELSE '' END,
             '&')) WITH ORDINALITY AS t(pair, ord)
    WHERE t.pair <> '';
$$;

COMMENT ON FUNCTION api.query_pairs(text) IS
    'The decoded key/value pairs of a URL''s query string, in order. A value keeps any further = and ?.';

-- Single-value query-string parser. hstore cannot hold duplicate keys, so for
-- a repeated key (?tag=a&tag=b) this keeps the FIRST occurrence. Use
-- api.query_params_multi() when a parameter may legitimately repeat.
CREATE OR REPLACE FUNCTION api.query_params(url text)
RETURNS extensions.hstore
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT COALESCE(
        extensions.hstore(array_agg(p.key ORDER BY p.ord), array_agg(p.value ORDER BY p.ord)),
        ''::extensions.hstore
    )
    FROM (
        SELECT DISTINCT ON (key) key, value, ord
        FROM api.query_pairs(url)
        ORDER BY key, ord
    ) p;
$$;

COMMENT ON FUNCTION api.query_params(text) IS
    'Single-value query-string parser (hstore). Repeated keys collapse to the first value; use api.query_params_multi() to preserve all values.';

-- Multi-value query-string parser. Returns jsonb mapping each key to a JSON
-- array of all its values in order, so ?tag=a&tag=b yields {"tag": ["a","b"]}.
-- A single-value param is a one-element array the caller unwraps with ->>0.
CREATE OR REPLACE FUNCTION api.query_params_multi(url text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT COALESCE(jsonb_object_agg(key, arr), '{}'::jsonb)
    FROM (
        SELECT key, jsonb_agg(value ORDER BY ord) AS arr
        FROM api.query_pairs(url)
        GROUP BY key
    ) grouped;
$$;

COMMENT ON FUNCTION api.query_params_multi(text) IS
    'Multi-value query-string parser (jsonb). Each key maps to a JSON array of all its values in order; preserves duplicate keys that api.query_params() would drop.';

-- A REST route declares its query parameters as querySchema: a JSON Schema
-- object in the dialect of inputSchema. The gateway enforces every keyword it
-- accepts here, so the published contract and the enforced one cannot differ;
-- a keyword it could not enforce is refused at registration.
CREATE OR REPLACE FUNCTION internal.query_property_problem(p_property jsonb, p_top boolean)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN jsonb_typeof(p_property) IS DISTINCT FROM 'object' THEN 'must be a schema object'
        WHEN EXISTS (SELECT 1 FROM jsonb_object_keys(p_property) k
                     WHERE k <> ALL (ARRAY['type', 'format', 'enum', 'minimum', 'maximum', 'description', 'default', 'items']))
            THEN 'supports type, format, enum, minimum, maximum, items, description and default'
        WHEN p_property->>'type' = 'array' AND p_top
            THEN internal.query_property_problem(p_property->'items', false)
        WHEN p_property ? 'items' THEN '"items" belongs to "type": "array"'
        WHEN COALESCE(p_property->>'type', 'string') <> ALL (ARRAY['string', 'integer', 'number', 'boolean'])
            THEN 'type must be string, integer, number or boolean' || CASE WHEN p_top THEN ', or an array of those' ELSE '' END
        WHEN p_property ? 'enum' AND (jsonb_typeof(p_property->'enum') IS DISTINCT FROM 'array'
             OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_property->'enum') e
                        WHERE jsonb_typeof(e) NOT IN ('string', 'number', 'boolean')))
            THEN 'enum must be an array of strings, numbers or booleans'
        WHEN p_property ? 'format' AND (COALESCE(p_property->>'type', 'string') <> 'string'
             OR COALESCE(p_property->>'format', '') <> ALL (ARRAY['uuid', 'date', 'date-time']))
            THEN 'format must be uuid, date or date-time, on type string'
        WHEN (p_property ? 'minimum' OR p_property ? 'maximum')
             AND COALESCE(p_property->>'type', 'string') NOT IN ('integer', 'number')
            THEN 'minimum and maximum need type integer or number'
        WHEN jsonb_typeof(COALESCE(p_property->'minimum', '0')) <> 'number'
             OR jsonb_typeof(COALESCE(p_property->'maximum', '0')) <> 'number'
            THEN 'minimum and maximum must be numbers'
    END;
$$;

CREATE OR REPLACE FUNCTION internal.query_schema_problem(p_schema jsonb)
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE
        WHEN jsonb_typeof(p_schema) IS DISTINCT FROM 'object' THEN 'querySchema must be a JSON Schema object'
        WHEN EXISTS (SELECT 1 FROM jsonb_object_keys(p_schema) k
                     WHERE k <> ALL (ARRAY['type', 'properties', 'required', 'description']))
            THEN 'querySchema supports type, properties, required and description at its root'
        WHEN p_schema->>'type' IS DISTINCT FROM 'object' THEN 'querySchema must have "type": "object"'
        WHEN jsonb_typeof(p_schema->'properties') IS DISTINCT FROM 'object' THEN 'querySchema needs "properties"'
        WHEN EXISTS (SELECT 1 FROM jsonb_array_elements_text(COALESCE(p_schema->'required', '[]')) r
                     WHERE NOT p_schema->'properties' ? r)
            THEN 'querySchema requires a parameter it does not declare'
        ELSE (SELECT string_agg(format('query parameter "%s": %s', p.key, p.problem), '; ' ORDER BY p.key)
              FROM (SELECT e.key, internal.query_property_problem(e.value, true) AS problem
                    FROM jsonb_each(p_schema->'properties') e) p
              WHERE p.problem IS NOT NULL)
    END;
$$;

-- A number as a query string spells it: 12, -0.5, .5, 1e3.
CREATE OR REPLACE FUNCTION internal.is_numeric_literal(p_value text)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT p_value ~ '^-?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][-+]?[0-9]{1,3})?$';
$$;

-- One query value against one scalar property schema. Enum values compare by
-- their text form, so an integer or boolean enum accepts ?size=20 or ?on=true.
CREATE OR REPLACE FUNCTION internal.query_value_is_valid(p_property jsonb, p_value text)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE COALESCE(p_property->>'type', 'string')
               WHEN 'integer' THEN p_value ~ '^-?[0-9]{1,18}$'
               WHEN 'number' THEN internal.is_numeric_literal(p_value)
               WHEN 'boolean' THEN p_value IN ('true', 'false')
               ELSE CASE p_property->>'format'
                        WHEN 'uuid' THEN p_value ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                        WHEN 'date' THEN p_value ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' AND pg_input_is_valid(p_value, 'date')
                        WHEN 'date-time' THEN p_value ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T' AND pg_input_is_valid(p_value, 'timestamptz')
                        ELSE true
                    END
           END
       AND (NOT p_property ? 'enum'
            OR EXISTS (SELECT 1 FROM jsonb_array_elements(p_property->'enum') e WHERE e #>> '{}' = p_value))
       AND (CASE WHEN p_property ? 'minimum' AND p_property->>'type' IN ('integer', 'number')
                      AND internal.is_numeric_literal(p_value)
                 THEN p_value::numeric >= (p_property->>'minimum')::numeric ELSE true END)
       AND (CASE WHEN p_property ? 'maximum' AND p_property->>'type' IN ('integer', 'number')
                      AND internal.is_numeric_literal(p_value)
                 THEN p_value::numeric <= (p_property->>'maximum')::numeric ELSE true END);
$$;

-- What a request's query violates, as RFC 9457 invalid-params: reason
-- "required" for a missing required parameter, "invalid" for a value that does
-- not fit. An empty value (?q=) counts as absent. A parameter declared as an
-- array may repeat; any other may not. NULL when the query fits.
CREATE OR REPLACE FUNCTION internal.query_schema_violations(p_schema jsonb, p_params jsonb)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_agg(jsonb_build_object('name', v.name, 'reason', v.reason) ORDER BY v.name)
    FROM (
        SELECT e.key AS name,
               CASE WHEN vals.n = 0 THEN 'required' ELSE 'invalid' END AS reason
        FROM jsonb_each(p_schema->'properties') e
        CROSS JOIN LATERAL (
            SELECT count(*) AS n,
                   bool_and(internal.query_value_is_valid(
                       CASE WHEN e.value->>'type' = 'array' THEN e.value->'items' ELSE e.value END, x.value)) AS valid
            FROM jsonb_array_elements_text(COALESCE(p_params->e.key, '[]')) AS x(value)
            WHERE x.value <> ''
        ) vals
        WHERE (vals.n = 0 AND COALESCE(p_schema->'required', '[]') ? e.key)
           OR (vals.n > 0 AND (NOT vals.valid OR (vals.n > 1 AND e.value->>'type' IS DISTINCT FROM 'array')))
    ) v;
$$;

COMMENT ON FUNCTION internal.query_schema_violations(jsonb, jsonb) IS
    'Checks parsed query parameters (api.query_params_multi) against a querySchema: required, type, enum (by text form), minimum, maximum. Returns invalid-params entries {name, reason: required|invalid}, or NULL.';

DO $$
DECLARE
    v_schema jsonb := '{"type": "object", "required": ["format"], "properties": {
        "format": {"type": "string", "enum": ["csv", "json"]},
        "size": {"type": "integer", "enum": [10, 20, 50]},
        "on": {"type": "boolean", "enum": [true]},
        "page": {"type": "integer", "minimum": 1},
        "since": {"type": "string", "format": "date"},
        "tag": {"type": "array", "items": {"type": "string"}}}}';
    v_violations jsonb;
BEGIN
    IF internal.query_schema_problem(v_schema) IS NOT NULL THEN
        RAISE EXCEPTION 'query schema: a valid schema was refused: %', internal.query_schema_problem(v_schema);
    END IF;
    IF internal.query_schema_problem('{"type": "object", "properties": {"q": {"type": "string", "pattern": "^a"}}}') IS NULL
       OR internal.query_schema_problem('{"type": "object", "properties": {"q": {"type": "object"}}}') IS NULL
       OR internal.query_schema_problem('{"type": "object", "required": ["x"], "properties": {}}') IS NULL THEN
        RAISE EXCEPTION 'query schema: a keyword the gateway cannot enforce must be refused';
    END IF;
    v_violations := internal.query_schema_violations(v_schema,
        api.query_params_multi('/r?format=csv&size=20&on=true&page=3&tag=a&tag=b&since=2026-10-31'));
    IF v_violations IS NOT NULL THEN
        RAISE EXCEPTION 'query schema: valid values (integer and boolean enums included) were refused: %', v_violations;
    END IF;
    v_violations := internal.query_schema_violations(v_schema,
        api.query_params_multi('/r?format=&size=30&on=false&page=0&since=2026-02-30'));
    IF v_violations IS DISTINCT FROM '[{"name": "format", "reason": "required"}, {"name": "on", "reason": "invalid"},
                                      {"name": "page", "reason": "invalid"}, {"name": "since", "reason": "invalid"},
                                      {"name": "size", "reason": "invalid"}]'::jsonb THEN
        RAISE EXCEPTION 'query schema: violations not reported as expected: %', v_violations;
    END IF;
END $$;

CREATE OR REPLACE FUNCTION api.url_path(url text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT split_part(url, '?', 1);
$$;

COMMENT ON FUNCTION api.url_path(text) IS
    'Extracts the path component from a URL by stripping the query string. A bare fragment is left in place on purpose: the http/https grammar (RFC 9110 §4.2.1) has no fragment component, so a literal "#" in a request target is malformed, and removing it would let /admin#x resolve to the route for /admin. A fragment behind a query is already gone with the query.';

-- One pass of RFC 3986 normalization kept whole: each step works on what the
-- one before left, and split apart the order would no longer be visible.
CREATE OR REPLACE FUNCTION api.canonical_path(p_path text)
RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT PARALLEL SAFE AS $$
DECLARE
    v_path text;
    v_result text;
    v_i int;
    v_len int;
    v_hex text;
    v_byte int;
    v_segments text[];
    v_out text[];
    v_seg text;
BEGIN
    v_path := split_part(p_path, '?', 1);
    IF v_path = '' THEN
        RETURN '/';
    END IF;

    -- §6.2.2.1 + §6.2.2.2: uppercase percent-encoding hex, decode unreserved
    v_result := '';
    v_i := 1;
    v_len := length(v_path);
    WHILE v_i <= v_len LOOP
        IF substring(v_path FROM v_i FOR 1) = '%' AND v_i + 2 <= v_len THEN
            v_hex := upper(substring(v_path FROM v_i + 1 FOR 2));
            IF v_hex ~ '^[0-9A-F]{2}$' THEN
                v_byte := get_byte(decode(v_hex, 'hex'), 0);
                IF (v_byte BETWEEN 65 AND 90)
                   OR (v_byte BETWEEN 97 AND 122)
                   OR (v_byte BETWEEN 48 AND 57)
                   OR v_byte IN (45, 46, 95, 126)
                THEN
                    v_result := v_result || chr(v_byte);
                ELSE
                    v_result := v_result || '%' || v_hex;
                END IF;
                v_i := v_i + 3;
                CONTINUE;
            END IF;
        END IF;
        v_result := v_result || substring(v_path FROM v_i FOR 1);
        v_i := v_i + 1;
    END LOOP;
    v_path := v_result;

    IF left(v_path, 1) IS DISTINCT FROM '/' THEN
        v_path := '/' || v_path;
    END IF;

    -- §6.2.2.3 / §5.2.4: remove dot-segments
    v_segments := string_to_array(v_path, '/');
    v_out := ARRAY[]::text[];
    FOREACH v_seg IN ARRAY v_segments LOOP
        IF v_seg = '.' THEN
            CONTINUE;
        ELSIF v_seg = '..' THEN
            IF COALESCE(array_length(v_out, 1), 0) > 1 THEN
                v_out := v_out[1:array_length(v_out, 1) - 1];
            END IF;
        ELSE
            v_out := array_append(v_out, v_seg);
        END IF;
    END LOOP;
    v_path := array_to_string(v_out, '/');

    -- pgmi policy: collapse duplicate slashes
    v_path := regexp_replace(v_path, '/+', '/', 'g');

    -- pgmi policy + §6.2.3: ensure leading slash
    IF v_path = '' OR left(v_path, 1) IS DISTINCT FROM '/' THEN
        v_path := '/' || v_path;
    END IF;

    -- pgmi policy: strip trailing slash except root
    IF v_path IS DISTINCT FROM '/' AND right(v_path, 1) = '/' THEN
        v_path := left(v_path, length(v_path) - 1);
    END IF;

    RETURN v_path;
END;
$$;

COMMENT ON FUNCTION api.canonical_path(text) IS
    'RFC 3986 syntax-based normalization plus pgmi policy: uppercases percent-encoding hex (§6.2.2.1), decodes unreserved characters (§6.2.2.2), removes dot-segments (§5.2.4), collapses duplicate slashes, ensures leading slash, strips trailing slash.';

DO $$
DECLARE
    v_params extensions.hstore;
    v_params_multi jsonb;
    v_path text;
    v_decoded text;
    v_quoted text;
BEGIN
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
    -- q=0 is an explicit refusal (RFC 9110 12.5.1), including through a
    -- wildcard. gateways/python/gateway.py answers the same question about the same
    -- header and must agree.
    IF api.accept_matches('application/json;q=0', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: q=0 is a refusal, not an acceptance';
    END IF;
    IF api.accept_matches('*/*;q=0', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: a wildcard refused with q=0 must not match';
    END IF;
    IF api.accept_matches('text/html, application/json;q=0', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: a refused range must not be rescued by an earlier one';
    END IF;
    IF api.accept_matches('application/json;q=0, */*', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: a refused type must not be rescued by a less specific wildcard';
    END IF;
    IF NOT api.accept_matches('application/*;q=0, application/json', ARRAY['application/json']) THEN
        RAISE EXCEPTION 'accept_matches: the exact type outranks a refused wildcard';
    END IF;
    IF NOT api.accept_matches('application/json;q=0, */*', ARRAY['application/json', 'text/csv']) THEN
        RAISE EXCEPTION 'accept_matches: a wildcard still accepts the types not refused by name';
    END IF;

    v_decoded := api.url_decode('John%20Doe');
    IF v_decoded IS DISTINCT FROM 'John Doe' THEN
        RAISE EXCEPTION 'url_decode percent failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('hello+world');
    IF v_decoded IS DISTINCT FROM 'hello world' THEN
        RAISE EXCEPTION 'url_decode plus failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('100%25+complete');
    IF v_decoded IS DISTINCT FROM '100% complete' THEN
        RAISE EXCEPTION 'url_decode mixed failed: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('%GG%20hello');
    IF v_decoded IS DISTINCT FROM '%GG hello' THEN
        RAISE EXCEPTION 'url_decode must skip invalid %% and decode later valid sequences: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('trailing%');
    IF v_decoded IS DISTINCT FROM 'trailing%' THEN
        RAISE EXCEPTION 'url_decode trailing %% should stay literal: got %', v_decoded;
    END IF;

    v_decoded := api.url_decode('trailing%1');
    IF v_decoded IS DISTINCT FROM 'trailing%1' THEN
        RAISE EXCEPTION 'url_decode trailing %%X should stay literal: got %', v_decoded;
    END IF;

    v_quoted := api.regexp_quote('file.json');
    IF v_quoted IS DISTINCT FROM 'file\.json' THEN
        RAISE EXCEPTION 'regexp_quote dot failed: got %', v_quoted;
    END IF;

    v_quoted := api.regexp_quote('a+b*c?');
    IF v_quoted IS DISTINCT FROM 'a\+b\*c\?' THEN
        RAISE EXCEPTION 'regexp_quote metachar failed: got %', v_quoted;
    END IF;

    v_quoted := api.uri_template_to_regex('/api/resource.json/{id}');
    IF v_quoted IS DISTINCT FROM '^/api/resource\.json/[^/]+$' THEN
        RAISE EXCEPTION 'uri_template_to_regex failed: got %', v_quoted;
    END IF;

    IF NOT '/api/resource.json/123' ~ api.uri_template_to_regex('/api/resource.json/{id}') THEN
        RAISE EXCEPTION 'uri_template_to_regex match failed';
    END IF;

    IF '/api/resourceXjson/123' ~ api.uri_template_to_regex('/api/resource.json/{id}') THEN
        RAISE EXCEPTION 'uri_template_to_regex should not match unescaped dot';
    END IF;

    v_params := api.query_params('/api/users?name=john&age=30');
    IF v_params->'name' IS DISTINCT FROM 'john' OR v_params->'age' IS DISTINCT FROM '30' THEN
        RAISE EXCEPTION 'query_params basic failed';
    END IF;

    v_params := api.query_params('/search?q=hello%20world&filter=a%2Bb');
    IF v_params->'q' IS DISTINCT FROM 'hello world' THEN
        RAISE EXCEPTION 'query_params url decode failed: got %', v_params->'q';
    END IF;
    IF v_params->'filter' IS DISTINCT FROM 'a+b' THEN
        RAISE EXCEPTION 'query_params url decode plus failed: got %', v_params->'filter';
    END IF;

    IF api.query_params('/f?t=YWJj==&next=/a?b=1&flag')
       IS DISTINCT FROM 't=>YWJj==, next=>/a?b=1, flag=>""'::extensions.hstore THEN
        RAISE EXCEPTION 'query_params must keep = and ? inside a value: got %',
            api.query_params('/f?t=YWJj==&next=/a?b=1&flag');
    END IF;

    -- query_params drops repeated keys (first wins); query_params_multi keeps all
    IF api.query_params('/f?tag=a&tag=b')->'tag' IS DISTINCT FROM 'a' THEN
        RAISE EXCEPTION 'query_params should keep first value for repeated key';
    END IF;

    v_params_multi := api.query_params_multi('/f?tag=a&tag=b&q=hi');
    IF v_params_multi->'tag' IS DISTINCT FROM '["a", "b"]'::jsonb THEN
        RAISE EXCEPTION 'query_params_multi must preserve duplicate keys: got %', v_params_multi->'tag';
    END IF;
    IF v_params_multi->'q'->>0 IS DISTINCT FROM 'hi' THEN
        RAISE EXCEPTION 'query_params_multi single value should be a one-element array: got %', v_params_multi->'q';
    END IF;

    v_path := api.url_path('/api/users?name=john');
    IF v_path IS DISTINCT FROM '/api/users' THEN
        RAISE EXCEPTION 'url_path failed';
    END IF;

    -- Keeping the fragment is the decision, not an oversight: stripping it
    -- would let a malformed /admin#x reach the route for /admin.
    IF api.url_path('/api/users#frag') IS DISTINCT FROM '/api/users#frag' THEN
        RAISE EXCEPTION 'url_path must not strip a bare fragment: got %',
            api.url_path('/api/users#frag');
    END IF;
    IF api.url_path('/api/users?name=john#frag') IS DISTINCT FROM '/api/users' THEN
        RAISE EXCEPTION 'url_path: a fragment behind a query goes with the query: got %',
            api.url_path('/api/users?name=john#frag');
    END IF;

    -- canonical_path: RFC 3986 normalization + pgmi policy
    IF api.canonical_path('/hello') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: canonical path must pass through unchanged';
    END IF;
    IF api.canonical_path('/hello?x=1') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must strip query string';
    END IF;
    IF api.canonical_path('/hel%6Co') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must decode unreserved %%6C → l';
    END IF;
    IF api.canonical_path('/hel%6co') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: lowercase hex must decode identically';
    END IF;
    IF api.canonical_path('/a/../hello') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must remove dot-segments';
    END IF;
    IF api.canonical_path('') IS DISTINCT FROM '/' THEN
        RAISE EXCEPTION 'canonical_path: empty path must become /';
    END IF;
    IF api.canonical_path('/hello/') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must strip trailing slash';
    END IF;
    IF api.canonical_path('hello') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must add leading slash';
    END IF;
    IF api.canonical_path('/hello//') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must collapse duplicate slashes';
    END IF;
    IF api.canonical_path('//hello') IS DISTINCT FROM '/hello' THEN
        RAISE EXCEPTION 'canonical_path: must collapse leading duplicate slashes';
    END IF;
    IF api.canonical_path('/') IS DISTINCT FROM '/' THEN
        RAISE EXCEPTION 'canonical_path: root must survive';
    END IF;
    IF api.canonical_path('/Users') IS DISTINCT FROM '/Users' THEN
        RAISE EXCEPTION 'canonical_path: path case must be preserved';
    END IF;
    IF api.canonical_path('/a%2Fb') IS DISTINCT FROM '/a%2Fb' THEN
        RAISE EXCEPTION 'canonical_path: %%2F must not become segment separator';
    END IF;
    IF api.canonical_path('/public/../admin') IS DISTINCT FROM '/admin' THEN
        RAISE EXCEPTION 'canonical_path: dot-segments must resolve before route match';
    END IF;
    IF api.canonical_path('/public/%2e%2e/admin') IS DISTINCT FROM '/admin' THEN
        RAISE EXCEPTION 'canonical_path: percent-encoded dot-segments must resolve';
    END IF;
    IF api.canonical_path('/a/%7E/b') IS DISTINCT FROM '/a/~/b' THEN
        RAISE EXCEPTION 'canonical_path: tilde is unreserved, must decode';
    END IF;
    IF api.canonical_path('/a/%20/b') IS DISTINCT FROM '/a/%20/b' THEN
        RAISE EXCEPTION 'canonical_path: space is reserved, must stay encoded';
    END IF;
    IF api.canonical_path('/a/%2D%2E%5F/b') IS DISTINCT FROM '/a/-._/b' THEN
        RAISE EXCEPTION 'canonical_path: dash dot underscore are unreserved, must decode';
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

-- RFC 9110 §15.5.2 makes at least one challenge mandatory on a 401. The scheme
-- is the transport's, so it is the deploy parameter www_authenticate.
DO $$
BEGIN
    EXECUTE format($fn$
        CREATE OR REPLACE FUNCTION api.www_authenticate_challenge()
        RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $body$
            SELECT %L::text;
        $body$
    $fn$, btrim(pg_temp.deployment_setting('www_authenticate')));
END $$;

COMMENT ON FUNCTION api.www_authenticate_challenge() IS
    'The challenge stamped on a 401 (RFC 9110 15.5.2): the deploy parameter www_authenticate, Session by default.';

-- extensions are RFC 9457 extension members; the standard members win on a
-- name clash.
CREATE OR REPLACE FUNCTION api.problem_response(
    status_code integer,
    title text,
    detail text DEFAULT NULL,
    type_uri text DEFAULT NULL,
    instance text DEFAULT NULL,
    code text DEFAULT NULL,
    invalid_params jsonb DEFAULT NULL,
    extensions jsonb DEFAULT NULL
) RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT (
        status_code,
        extensions.hstore('content-type', 'application/problem+json'),
        convert_to(
            (COALESCE(extensions, '{}'::jsonb) || jsonb_strip_nulls(jsonb_build_object(
                'type', COALESCE(type_uri, 'about:blank'),
                'title', title,
                'status', status_code,
                'detail', detail,
                'instance', instance,
                'code', code,
                'invalid-params', invalid_params
            )))::text,
            'UTF8'
        )
    )::api.http_response;
$$;

COMMENT ON FUNCTION api.problem_response(integer, text, text, text, text, text, jsonb, jsonb) IS
    'RFC 9457 problem+json response. code is a stable machine-readable token (framework codes are pgmi.<reason>, application codes <schema>.<entity>.<reason>); invalid_params is a jsonb array of {name,reason} (build with api.invalid_param); extensions is an object of extra members. NULL members are stripped.';

-- RFC 9457 invalid-params member constructor: one {name, reason} entry.
CREATE OR REPLACE FUNCTION api.invalid_param(p_name text, p_reason text)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object('name', p_name, 'reason', p_reason);
$$;

COMMENT ON FUNCTION api.invalid_param(text, text) IS
    'Builds one RFC 9457 invalid-params entry {name, reason}; aggregate several into a jsonb array for api.problem_response(..., invalid_params => ...).';

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.json_response(200, '{"status": "ok"}'::jsonb);
    IF (v_response).status_code IS DISTINCT FROM 200 THEN
        RAISE EXCEPTION 'json_response status failed';
    END IF;

    v_response := api.problem_response(404, 'Not Found', 'Resource does not exist');
    v_content := api.content_json((v_response).content);
    IF v_content->>'title' IS DISTINCT FROM 'Not Found' THEN
        RAISE EXCEPTION 'problem_response failed';
    END IF;
    IF v_content ? 'code' OR v_content ? 'invalid-params' THEN
        RAISE EXCEPTION 'problem_response should strip absent code/invalid-params members';
    END IF;

    -- RFC 9457 fields surface when provided
    v_response := api.problem_response(
        422, 'Unprocessable', 'Validation failed',
        code => 'pgmi.invalid_value',
        invalid_params => jsonb_build_array(api.invalid_param('total', 'must be positive')),
        extensions => jsonb_build_object('requiredReadOnly', true, 'status', 999)
    );
    v_content := api.content_json((v_response).content);
    IF v_content->>'code' IS DISTINCT FROM 'pgmi.invalid_value' THEN
        RAISE EXCEPTION 'problem_response code member missing';
    END IF;
    IF v_content->'invalid-params'->0->>'name' IS DISTINCT FROM 'total' THEN
        RAISE EXCEPTION 'problem_response invalid-params member missing';
    END IF;
    IF v_content->'requiredReadOnly' IS DISTINCT FROM 'true'::jsonb OR (v_content->>'status')::int IS DISTINCT FROM 422 THEN
        RAISE EXCEPTION 'problem_response extensions must be merged under the standard members, got %', v_content;
    END IF;
END $$;

-- ============================================================================
-- List Pagination
-- ============================================================================
-- Uniform ?limit/?offset parsing for list handlers: clamps limit to [0, max]
-- and offset to >= 0, and returns a ready 422 (pgmi.invalid_pagination) in o_error when either value is
-- present but not an integer. A route that declares api.page_query_schema
-- never sees o_error: the dispatcher refuses such a value first. Any other
-- caller checks `(o_error).status_code IS NOT NULL` and returns o_error before
-- running the query. Fetch o_limit + 1 rows to derive
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
                THEN api.problem_response(422, 'Unprocessable Entity', 'limit must be an integer', code => 'pgmi.invalid_pagination',
                        invalid_params => jsonb_build_array(api.invalid_param('limit', 'invalid')))
            WHEN NULLIF(p_q->'offset', '') IS NOT NULL
                 AND common.try_cast(p_q->'offset', NULL::integer) IS NULL
                THEN api.problem_response(422, 'Unprocessable Entity', 'offset must be an integer', code => 'pgmi.invalid_pagination',
                        invalid_params => jsonb_build_array(api.invalid_param('offset', 'invalid')))
            ELSE NULL
        END;
$$;

COMMENT ON FUNCTION api.pagination_params(extensions.hstore, integer, integer) IS
    'Parses ?limit/?offset from a query hstore: clamps limit to [0,p_max] (default p_default) and offset to >=0; sets o_error to a 422 pgmi.invalid_pagination problem when either is present but non-integer. A route declaring api.page_query_schema never sees o_error (the dispatcher refuses first); any other caller returns o_error when its status_code is not null.';

DO $$
DECLARE
    v_page record;
BEGIN
    -- Defaults when nothing supplied
    v_page := api.pagination_params(''::extensions.hstore);
    IF v_page.o_limit IS DISTINCT FROM 50 OR v_page.o_offset IS DISTINCT FROM 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params defaults failed (limit=%, offset=%)', v_page.o_limit, v_page.o_offset;
    END IF;

    -- Clamp to max and floor offset at 0
    v_page := api.pagination_params('limit=>5000, offset=>-3'::extensions.hstore);
    IF v_page.o_limit IS DISTINCT FROM 200 OR v_page.o_offset IS DISTINCT FROM 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params clamping failed (limit=%, offset=%)', v_page.o_limit, v_page.o_offset;
    END IF;

    v_page := api.pagination_params('limit=>abc'::extensions.hstore);
    IF (v_page.o_error).status_code IS DISTINCT FROM 422
       OR api.content_json((v_page.o_error).content)->>'code' IS DISTINCT FROM 'pgmi.invalid_pagination' THEN
        RAISE EXCEPTION 'pagination_params should answer 422 pgmi.invalid_pagination on a non-integer limit, got %', (v_page.o_error).status_code;
    END IF;

    -- limit=0 is honored (empty page), not treated as missing
    v_page := api.pagination_params('limit=>0'::extensions.hstore);
    IF v_page.o_limit IS DISTINCT FROM 0 OR (v_page.o_error).status_code IS NOT NULL THEN
        RAISE EXCEPTION 'pagination_params should honor limit=0, got %', v_page.o_limit;
    END IF;
END $$;

-- Optional page helper for list routes. The three pieces agree by
-- construction: page_query_schema declares limit and offset (the gateway
-- enforces them), api.pagination_params reads them, page_response answers the
-- envelope page_output_schema declares. lib/__test__/test_response_contract.sql
-- holds every route that declares the envelope to that.
CREATE OR REPLACE FUNCTION api.page_query_schema(
    p_properties    jsonb DEFAULT '{}'::jsonb,
    p_default_limit integer DEFAULT 50,
    p_max_limit     integer DEFAULT 200
) RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object('type', 'object', 'properties', COALESCE(p_properties, '{}'::jsonb) || jsonb_build_object(
        'limit', jsonb_build_object('type', 'integer', 'minimum', 0, 'maximum', p_max_limit, 'default', p_default_limit),
        'offset', jsonb_build_object('type', 'integer', 'minimum', 0, 'maximum', 2147483647, 'default', 0)));
$$;

COMMENT ON FUNCTION api.page_query_schema(jsonb, integer, integer) IS
    'The querySchema of a list route: p_properties plus limit (0..p_max_limit, default p_default_limit) and offset (0..2147483647, default 0); the dispatcher refuses any other value with 400 before the handler runs.';

CREATE OR REPLACE FUNCTION api.page_output_schema(p_item jsonb DEFAULT NULL)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
        'type', 'object',
        'properties', jsonb_build_object(
            'items', jsonb_build_object('type', 'array', 'items', COALESCE(p_item, '{"type": "object"}'::jsonb)),
            'total', jsonb_build_object('type', 'integer', 'minimum', 0),
            'limit', jsonb_build_object('type', 'integer', 'minimum', 0),
            'offset', jsonb_build_object('type', 'integer', 'minimum', 0),
            'hasMore', jsonb_build_object('type', 'boolean')),
        'required', jsonb_build_array('items', 'total', 'limit', 'offset', 'hasMore'));
$$;

COMMENT ON FUNCTION api.page_output_schema(jsonb) IS
    'The outputSchema of a list route answering api.page_response: items (each p_item, an object when NULL), total, limit, offset, hasMore, all required.';

CREATE OR REPLACE FUNCTION api.page_response(p_items jsonb, p_total bigint, p_limit integer, p_offset integer)
RETURNS api.http_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.json_response(200, jsonb_build_object(
        'items', COALESCE(p_items, '[]'::jsonb),
        'total', p_total,
        'limit', p_limit,
        'offset', p_offset,
        'hasMore', p_offset + jsonb_array_length(COALESCE(p_items, '[]'::jsonb)) < p_total));
$$;

COMMENT ON FUNCTION api.page_response(jsonb, bigint, integer, integer) IS
    'A 200 page of a list: {items, total, limit, offset, hasMore}; hasMore when rows follow this page. Declare it with api.page_output_schema and api.page_query_schema.';

DO $$
DECLARE
    v_body jsonb := api.content_json((api.page_response('[1, 2]', 5, 2, 2)).content);
BEGIN
    IF v_body IS DISTINCT FROM '{"items": [1, 2], "total": 5, "limit": 2, "offset": 2, "hasMore": true}'::jsonb THEN
        RAISE EXCEPTION 'page_response: middle page wrong: %', v_body;
    END IF;
    v_body := api.content_json((api.page_response(NULL, 4, 2, 4)).content);
    IF v_body->'items' IS DISTINCT FROM '[]'::jsonb OR v_body->'hasMore' IS DISTINCT FROM 'false'::jsonb THEN
        RAISE EXCEPTION 'page_response: an empty last page must have items [] and no more: %', v_body;
    END IF;
END $$;

-- The validate step for typed body fields: every field named in p_types that
-- is present and not blank but does not read as its PostgreSQL type, as
-- invalid-params with reason "invalid". Absent, null and blank fields are the
-- required check's business, not this one's. NaN and Infinity are not numbers
-- a client means. NULL when everything reads.
CREATE OR REPLACE FUNCTION api.malformed_params(p_input jsonb, p_types jsonb)
RETURNS jsonb
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT jsonb_agg(api.invalid_param(t.key, 'invalid') ORDER BY t.key)
    FROM jsonb_each_text(p_types) AS t(key, type_name)
    CROSS JOIN LATERAL (SELECT btrim(p_input->>t.key) AS value) v
    WHERE jsonb_typeof(p_input) = 'object'
      AND v.value <> ''
      AND (NOT pg_input_is_valid(v.value, t.type_name)
           OR (t.type_name IN ('numeric', 'real', 'double precision', 'float4', 'float8')
               AND lower(v.value) ~ '^[-+]?(nan|inf|infinity)$'));
$$;

COMMENT ON FUNCTION api.malformed_params(jsonb, jsonb) IS
    'Fields of p_input that are present but do not read as the PostgreSQL type p_types names for them ({"dueOn": "date", "projectId": "uuid"}), as invalid-params {name, reason: invalid}; NULL when all read. A handler answers 422 with invalid_params => the result.';

DO $$
DECLARE
    v_types jsonb := '{"projectId": "uuid", "dueOn": "date", "estimate": "numeric", "done": "boolean", "position": "integer"}';
BEGIN
    IF api.malformed_params('{}', v_types) IS NOT NULL
       OR api.malformed_params(NULL, v_types) IS NOT NULL
       OR api.malformed_params('{"dueOn": null, "estimate": "  ", "done": ""}', v_types) IS NOT NULL THEN
        RAISE EXCEPTION 'malformed_params: absent, null and blank fields are not malformed';
    END IF;
    IF api.malformed_params('{"projectId": " 019a0000-0000-7000-8000-000000000001 ", "dueOn": "2026-10-31",
                              "estimate": 2.5, "done": true, "position": "3", "other": "x"}', v_types) IS NOT NULL THEN
        RAISE EXCEPTION 'malformed_params: well-formed fields were reported';
    END IF;
    IF api.malformed_params('{"projectId": "abc", "dueOn": "2026-13-01", "estimate": "NaN", "done": "maybe", "position": "1.5"}', v_types)
       IS DISTINCT FROM '[{"name": "done", "reason": "invalid"}, {"name": "dueOn", "reason": "invalid"},
                          {"name": "estimate", "reason": "invalid"}, {"name": "position", "reason": "invalid"},
                          {"name": "projectId", "reason": "invalid"}]'::jsonb THEN
        RAISE EXCEPTION 'malformed_params: every malformed field must be reported, got %',
            api.malformed_params('{"projectId": "abc", "dueOn": "2026-13-01", "estimate": "NaN", "done": "maybe", "position": "1.5"}', v_types);
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
    id jsonb DEFAULT NULL,
    data jsonb DEFAULT NULL
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
            WHEN -32010 THEN 409
            WHEN -32011 THEN 422
            ELSE 500
        END,
        jsonb_build_object(
            'jsonrpc', '2.0',
            'error', jsonb_build_object('code', code, 'message', message)
                     || CASE WHEN data IS NULL THEN '{}' ELSE jsonb_build_object('data', data) END,
            'id', id
        )
    );
$$;

COMMENT ON FUNCTION api.jsonrpc_error(integer, text, jsonb, jsonb) IS
    'JSON-RPC 2.0 error response with optional error.data. Maps -32700..-32603 and -32001 to HTTP statuses, and the two refusals of a handler body: -32010 (the state refuses, 409, data.code) and -32011 (the input is invalid, 422, data.invalid-params).';

DO $$
DECLARE
    v_response api.http_response;
    v_content jsonb;
BEGIN
    v_response := api.jsonrpc_success('{"value": 42}'::jsonb, '"req-1"'::jsonb);
    v_content := api.content_json((v_response).content);
    IF v_content->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'jsonrpc_success failed';
    END IF;

    v_response := api.jsonrpc_error(-32601, 'Method not found');
    IF (v_response).status_code IS DISTINCT FROM 404 THEN
        RAISE EXCEPTION 'jsonrpc_error status mapping failed: expected 404, got %', (v_response).status_code;
    END IF;

    v_response := api.jsonrpc_error(-32010, 'Refused', '1', '{"code": "x"}');
    v_content := api.content_json((v_response).content);
    IF (v_response).status_code IS DISTINCT FROM 409 OR v_content->'error'->'data'->>'code' IS DISTINCT FROM 'x' THEN
        RAISE EXCEPTION 'jsonrpc_error: -32010 must be 409 with error.data, got % %', (v_response).status_code, v_content;
    END IF;
    IF (api.jsonrpc_error(-32011, 'Invalid')).status_code IS DISTINCT FROM 422
       OR (api.jsonrpc_error(-32602, 'Invalid params')).status_code IS DISTINCT FROM 400
       OR api.content_json((api.jsonrpc_error(-32602, 'Invalid params')).content)->'error' ? 'data' THEN
        RAISE EXCEPTION 'jsonrpc_error: -32011 must be 422, -32602 400 without data';
    END IF;
END $$;

-- ============================================================================
-- MCP Response Builders (JSON-RPC 2.0 Compliant)
-- ============================================================================
-- request_id is jsonb so JSON-RPC id types (string, integer, null) round-trip
-- exactly.

-- The server's identity, named in every success result's _meta
-- (io.modelcontextprotocol/serverInfo) and in a legacy initialize result.
-- SET mcp.server_name / mcp.server_version to override the database name and 1.0.0.
CREATE OR REPLACE FUNCTION api.mcp_server_info()
RETURNS jsonb
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
        'name', COALESCE(NULLIF(current_setting('mcp.server_name', true), ''), current_database()),
        'version', COALESCE(NULLIF(current_setting('mcp.server_version', true), ''), '1.0.0')
    );
$$;

COMMENT ON FUNCTION api.mcp_server_info() IS
    'Server identity: in every MCP success result''s _meta (io.modelcontextprotocol/serverInfo) and the legacy initialize result. Configurable via mcp.server_name / mcp.server_version; defaults to the database name and 1.0.0.';

-- The ttlMs and cacheScope of the cacheable results: the four lists and
-- resources/read. Private, because every list is filtered by the caller's
-- access; five minutes, because the registry changes only at a deploy and
-- every call decides access again.
CREATE OR REPLACE FUNCTION api.mcp_cache_policy()
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT '{"ttlMs": 300000, "cacheScope": "private"}'::jsonb;
$$;

COMMENT ON FUNCTION api.mcp_cache_policy() IS
    'ttlMs and cacheScope of the cacheable MCP results (tools/list, prompts/list, resources/list, resources/templates/list, resources/read): five minutes, private to the caller.';

CREATE OR REPLACE FUNCTION api.mcp_success(
    result jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT ROW(
        jsonb_build_object(
            'jsonrpc', '2.0',
            'id', request_id,
            'result', '{"resultType": "complete"}'::jsonb || result
                || jsonb_build_object('_meta', COALESCE(result->'_meta', '{}'::jsonb)
                       || jsonb_build_object('io.modelcontextprotocol/serverInfo', api.mcp_server_info()))
        )
    )::api.mcp_response;
$$;

COMMENT ON FUNCTION api.mcp_success(jsonb, jsonb) IS
    'MCP JSON-RPC 2.0 success envelope. The result object gains resultType "complete" (unless it names its own) and _meta["io.modelcontextprotocol/serverInfo"], its own _meta keys kept. request_id is jsonb to preserve the original JSON type.';

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
            'error', jsonb_build_object('code', code, 'message', COALESCE(message, 'Error'))
                || CASE WHEN data IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('data', data) END
        )
    )::api.mcp_response;
$$;

COMMENT ON FUNCTION api.mcp_error(integer, text, jsonb, jsonb) IS
    'MCP JSON-RPC 2.0 error envelope. Use for protocol-level errors (parse, method-not-found); tool execution failures use api.mcp_tool_error instead. data is kept as given, nulls inside it included.';

CREATE OR REPLACE FUNCTION api.mcp_tool_result(
    content jsonb,
    request_id jsonb,
    is_error boolean DEFAULT false,
    structured_content jsonb DEFAULT NULL
) RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('content', content)
        || CASE WHEN is_error IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('isError', is_error) END
        || CASE WHEN structured_content IS NULL THEN '{}'::jsonb
                ELSE jsonb_build_object('structuredContent', structured_content) END,
        request_id
    );
$$;

COMMENT ON FUNCTION api.mcp_tool_result(jsonb, jsonb, boolean, jsonb) IS
    'MCP tool invocation result. is_error signals application-level failure (still uses the success envelope per MCP spec). structured_content carries typed output when the tool declares an outputSchema; nulls inside it are kept.';

CREATE OR REPLACE FUNCTION api.mcp_tool_error(
    message text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT api.mcp_tool_result(
        jsonb_build_array(jsonb_build_object('type', 'text', 'text', message)),
        request_id,
        true
    );
$$;

COMMENT ON FUNCTION api.mcp_tool_error(text, jsonb) IS
    'Convenience wrapper: builds an MCP tool result with isError=true and the message as a text content item.';

-- An MCP answer failed when it is a JSON-RPC error or a tool result with
-- isError: the gateway rolls the handler back, and the exchange views count it.
CREATE OR REPLACE FUNCTION internal.mcp_is_error(p_envelope jsonb)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT COALESCE(p_envelope->>'error' IS NOT NULL OR p_envelope->'result'->'isError' = 'true'::jsonb, false);
$$;

CREATE OR REPLACE FUNCTION api.mcp_resource_result(
    contents jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT api.mcp_success(
        jsonb_build_object('contents', contents) || api.mcp_cache_policy(),
        request_id
    );
$$;

COMMENT ON FUNCTION api.mcp_resource_result(jsonb, jsonb) IS
    'MCP resources/read success response. contents is the JSON array of resource content items; ttlMs and cacheScope from api.mcp_cache_policy().';

CREATE OR REPLACE FUNCTION api.mcp_resource_error(
    message text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32603, message, request_id);
$$;

COMMENT ON FUNCTION api.mcp_resource_error(text, jsonb) IS
    'MCP resource error. Wraps the message as a -32603 internal error in the JSON-RPC error envelope.';

CREATE OR REPLACE FUNCTION api.mcp_resource_not_found(
    uri text,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT api.mcp_error(-32602, 'Resource not found', request_id, jsonb_build_object('uri', uri));
$$;

COMMENT ON FUNCTION api.mcp_resource_not_found(text, jsonb) IS
    'MCP resources/read answer for a resource that does not exist: -32602 with data.uri. An empty contents array is turned into the same answer.';

CREATE OR REPLACE FUNCTION api.mcp_prompt_result(
    messages jsonb,
    request_id jsonb
) RETURNS api.mcp_response
LANGUAGE sql STABLE PARALLEL SAFE AS $$
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
    v_response := api.mcp_success('{"value": 42}'::jsonb, '"req-1"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'mcp_success: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' IS DISTINCT FROM 'req-1' OR jsonb_typeof(v_envelope->'id') IS DISTINCT FROM 'string' THEN
        RAISE EXCEPTION 'mcp_success: id type not preserved as string';
    END IF;
    IF v_envelope->'result' IS NULL THEN
        RAISE EXCEPTION 'mcp_success: missing result';
    END IF;

    -- Test mcp_success with integer id — JSON-RPC 2.0 requires type preservation
    v_response := api.mcp_success('{}'::jsonb, '42'::jsonb);
    v_envelope := (v_response).envelope;
    IF jsonb_typeof(v_envelope->'id') IS DISTINCT FROM 'number' OR (v_envelope->>'id')::int IS DISTINCT FROM 42 THEN
        RAISE EXCEPTION 'mcp_success: integer id must stay numeric, got %', v_envelope->'id';
    END IF;

    v_response := api.mcp_error(-32603, 'Internal error', '"req-2"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
        RAISE EXCEPTION 'mcp_error: missing jsonrpc 2.0';
    END IF;
    IF v_envelope->>'id' IS DISTINCT FROM 'req-2' THEN
        RAISE EXCEPTION 'mcp_error: wrong id';
    END IF;
    IF (v_envelope->'error'->>'code')::int IS DISTINCT FROM -32603 THEN
        RAISE EXCEPTION 'mcp_error: wrong error code';
    END IF;

    v_response := api.mcp_tool_result('[{"type": "text", "text": "Hello"}]'::jsonb, '"req-3"'::jsonb);
    v_envelope := (v_response).envelope;
    IF v_envelope->>'jsonrpc' IS DISTINCT FROM '2.0' THEN
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
       OR (v_envelope->'result'->'structuredContent'->>'answer')::int IS DISTINCT FROM 42 THEN
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

    -- Every success result is complete and names the server; a handler's own
    -- _meta keys stay beside it.
    v_envelope := (api.mcp_success('{"_meta": {"x": 1}}'::jsonb, '"m"'::jsonb)).envelope;
    IF v_envelope->'result'->>'resultType' IS DISTINCT FROM 'complete'
       OR v_envelope->'result'->'_meta'->'io.modelcontextprotocol/serverInfo'->>'name' IS DISTINCT FROM current_database()
       OR v_envelope->'result'->'_meta'->'x' IS DISTINCT FROM '1'::jsonb THEN
        RAISE EXCEPTION 'mcp_success: resultType, serverInfo or the own _meta missing: %', v_envelope;
    END IF;

    -- Nulls inside structuredContent and error.data are data, not absent members.
    v_envelope := (api.mcp_tool_result('[]'::jsonb, '"n"'::jsonb, false, '{"due": null, "items": [null]}'::jsonb)).envelope;
    IF v_envelope->'result'->'structuredContent' IS DISTINCT FROM '{"due": null, "items": [null]}'::jsonb THEN
        RAISE EXCEPTION 'mcp_tool_result: nulls inside structuredContent were dropped: %', v_envelope;
    END IF;
    v_envelope := (api.mcp_error(-32022, 'm', '"e"'::jsonb, '{"requested": null}'::jsonb)).envelope;
    IF v_envelope->'error'->'data' IS DISTINCT FROM '{"requested": null}'::jsonb THEN
        RAISE EXCEPTION 'mcp_error: nulls inside data were dropped: %', v_envelope;
    END IF;
    IF (api.mcp_error(-1, NULL, '"e"'::jsonb)).envelope->'error' ? 'data'
       OR (api.mcp_error(-1, NULL, '"e"'::jsonb)).envelope->'error'->>'message' IS DISTINCT FROM 'Error' THEN
        RAISE EXCEPTION 'mcp_error: an absent data member or the NULL-message fallback changed';
    END IF;

    -- Only resources/read of the results built here is cacheable.
    v_envelope := (api.mcp_resource_result('[]'::jsonb, '"r"'::jsonb)).envelope;
    IF NOT v_envelope->'result' @> api.mcp_cache_policy() THEN
        RAISE EXCEPTION 'mcp_resource_result: ttlMs/cacheScope missing: %', v_envelope;
    END IF;
    IF (api.mcp_tool_result('[]'::jsonb, '"t"'::jsonb)).envelope->'result' ?| ARRAY['ttlMs', 'cacheScope']
       OR (api.mcp_prompt_result('[]'::jsonb, '"p"'::jsonb)).envelope->'result' ?| ARRAY['ttlMs', 'cacheScope'] THEN
        RAISE EXCEPTION 'a tool or prompt result must not carry cache members';
    END IF;

    v_text := api.mcp_text('Hello world');
    IF v_text->>'type' IS DISTINCT FROM 'text' OR v_text->>'text' IS DISTINCT FROM 'Hello world' THEN
        RAISE EXCEPTION 'mcp_text failed';
    END IF;
END $$;
