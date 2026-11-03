/*
<pgmi-meta
    id="a7f01000-0008-4000-8000-000000000001"
    idempotent="true">
  <description>
    Handler registration functions for REST, RPC, and MCP protocols
  </description>
  <sortKeys>
    <key>004/008</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing handler registration functions'; END $$;

-- ============================================================================
-- Shared: Handler Name Validation
-- ============================================================================
-- Reject handler names that would: (a) collide on PostgreSQL's 63-byte
-- identifier truncation when prefixed (rest_/rpc_/mcp_tool_), or (b) embed
-- characters that escape the format('%I') quoting we rely on. ASCII-only;
-- internationalisation is intentionally out of scope.

CREATE OR REPLACE FUNCTION internal.validate_handler_name(p_name text)
RETURNS void
LANGUAGE plpgsql STABLE STRICT PARALLEL SAFE AS $$
BEGIN
    IF p_name IS NULL OR length(p_name) = 0 THEN
        RAISE EXCEPTION 'handler name must be non-empty';
    END IF;
    IF p_name !~ '^[a-zA-Z][a-zA-Z0-9_.\-]{0,48}$' THEN
        RAISE EXCEPTION 'invalid handler name %; must match ^[a-zA-Z][a-zA-Z0-9_.-]{0,48}$ (1-49 chars, ASCII alnum/underscore/dot/hyphen, leading letter)',
            p_name
            USING HINT = 'PostgreSQL identifier limit is 63 bytes; pgmi caps at 49 to leave room for prefixes (rest_/rpc_/mcp_tool_) without silent truncation that would cause function-name collisions.';
    END IF;
END;
$$;

COMMENT ON FUNCTION internal.validate_handler_name(text) IS
    'Rejects handler names that risk PostgreSQL identifier truncation (>49 chars) or break format(%I) quoting. ASCII-only.';

-- validate_handler_name guards one way two names become one function: truncation.
-- This guards the other: RPC and MCP fold '.' and '-' to '_', so "reports.run",
-- "reports-run" and "reports_run" all become rpc_reports_run. Without it the
-- second registration replaced the first handler's body, then failed on
-- api.handler's unique handler_func with a message naming neither method.
CREATE OR REPLACE FUNCTION internal.assert_function_name_free(
    p_id uuid,
    p_schema text,
    p_function_name text,
    p_request_type text,
    p_name text
) RETURNS void
LANGUAGE plpgsql STABLE AS $$
DECLARE
    v_owner_id uuid;
    v_owner_name text;
BEGIN
    SELECT h.object_id, COALESCE(rr.method_name, mr.mcp_name)
    INTO v_owner_id, v_owner_name
    FROM api.handler h
    LEFT JOIN api.rpc_route rr ON rr.handler_object_id = h.object_id
    LEFT JOIN api.mcp_route mr ON mr.handler_object_id = h.object_id
    WHERE h.handler_func = to_regprocedure(format('%I.%I(%s)', p_schema, p_function_name, p_request_type))
      AND h.object_id <> p_id
    LIMIT 1;

    IF v_owner_id IS NOT NULL THEN
        RAISE EXCEPTION '"%" and "%" (handler %) both become function %.%', p_name, v_owner_name, v_owner_id, p_schema, p_function_name
            USING ERRCODE = 'unique_violation',
                  HINT = 'Dots and hyphens in a name become underscores in its function name. Rename one of the two.';
    END IF;
END;
$$;

-- ============================================================================
-- Shared: Random Dollar-Quote Boundary
-- ============================================================================
-- Avoid predictable boundaries (UUID-derived) so a malicious handler body
-- cannot pre-compute a sentinel that breaks out of the dollar-quoted block
-- during EXECUTE. Loops if the random nonce happens to appear in the body
-- (vanishingly unlikely with 64-bit entropy).

CREATE OR REPLACE FUNCTION internal.random_dollar_quote_boundary(p_body text)
RETURNS text
LANGUAGE plpgsql VOLATILE STRICT PARALLEL SAFE AS $$
DECLARE
    v_boundary text;
    v_attempts int := 0;
BEGIN
    LOOP
        v_boundary := 'hb_' || encode(extensions.gen_random_bytes(8), 'hex');
        IF position('$' || v_boundary || '$' IN p_body) = 0 THEN
            RETURN v_boundary;
        END IF;
        v_attempts := v_attempts + 1;
        IF v_attempts > 8 THEN
            RAISE EXCEPTION 'could not generate non-colliding dollar-quote boundary after % attempts', v_attempts;
        END IF;
    END LOOP;
END;
$$;

COMMENT ON FUNCTION internal.random_dollar_quote_boundary(text) IS
    'Returns a fresh dollar-quote tag (hb_<16 hex>) guaranteed not to appear inside p_body. Eliminates predictable-boundary injection paths in CREATE FUNCTION assembly.';

-- ============================================================================
-- Shared: Capture pg_proc Snapshot
-- ============================================================================

CREATE OR REPLACE FUNCTION internal.capture_handler_proc_snapshot(
    p_handler_oid oid,
    OUT handler_canonical text,
    OUT returns_type regtype,
    OUT returns_set boolean,
    OUT volatility text,
    OUT parallel text,
    OUT leakproof boolean,
    OUT security text,
    OUT language_name text,
    OUT owner_name name
)
LANGUAGE sql STABLE AS $$
    SELECT
        pg_get_functiondef(p.oid),
        p.prorettype::regtype,
        p.proretset,
        CASE p.provolatile
            WHEN 'i' THEN 'immutable'
            WHEN 's' THEN 'stable'
            WHEN 'v' THEN 'volatile'
        END,
        CASE p.proparallel
            WHEN 's' THEN 'safe'
            WHEN 'r' THEN 'restricted'
            WHEN 'u' THEN 'unsafe'
        END,
        p.proleakproof,
        CASE p.prosecdef WHEN true THEN 'definer' ELSE 'invoker' END,
        l.lanname,
        pg_get_userbyid(p.proowner)
    FROM pg_proc p
    JOIN pg_language l ON l.oid = p.prolang
    WHERE p.oid = p_handler_oid;
$$;

-- ============================================================================
-- Shared: Drop a Renamed Handler's Orphaned Function
-- ============================================================================
-- Re-registering an existing handler id under a new name points the registry
-- at a new pg_proc entry but leaves the old function behind — unreachable
-- through the gateway yet still callable and accumulating on every rename.
-- Drop the previous function before creating the replacement so renames do not
-- leak dead code. p_request_type is the (template-controlled) argument type;
-- it is interpolated with %s because regtype names must not be %I-quoted.

CREATE OR REPLACE FUNCTION internal.drop_renamed_handler(
    p_object_id uuid,
    p_schema text,
    p_new_function_name text,
    p_request_type text
) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_old text;
BEGIN
    SELECT handler_function_name INTO v_old
    FROM api.handler
    WHERE object_id = p_object_id;

    IF v_old IS NOT NULL AND v_old <> p_new_function_name THEN
        EXECUTE format('DROP FUNCTION IF EXISTS %I.%I(%s)', p_schema, v_old, p_request_type);
    END IF;
END;
$$;

COMMENT ON FUNCTION internal.drop_renamed_handler(uuid, text, text, text) IS
    'Drops the previously-registered handler function for an object_id when the new registration uses a different function name, preventing orphaned pg_proc entries on rename.';

-- ============================================================================
-- Shared: Declared Access
-- ============================================================================
-- Every registration declares exactly one of
--   "access": "public" | "authenticated"
--   "permissions": ["resource.action", ...]   any one of them opens the handler
-- A forgotten declaration fails the deploy instead of meaning "open". A
-- permission must already exist in the application's vocabulary
-- (internal.is_platform_permission), so a typo fails too.
CREATE OR REPLACE FUNCTION internal.declared_access(
    p_metadata jsonb,
    p_label    text,
    OUT access text,
    OUT permissions text[]
)
LANGUAGE plpgsql STABLE
SET search_path = api, internal, common, pg_temp
AS $$
DECLARE
    v_unknown text;
BEGIN
    IF p_metadata ? 'requiresAuth' THEN
        RAISE EXCEPTION 'Handler % declares "requiresAuth", which is not a handler key', p_label
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Declare "access": "public" or "authenticated", or "permissions": [...].';
    END IF;
    IF (p_metadata ? 'access') = (p_metadata ? 'permissions') THEN
        RAISE EXCEPTION 'Handler % must declare exactly one of "access" and "permissions"', p_label
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = '"permissions": ["resource.action", ...] for an operation; "access": "authenticated" for any signed-in caller; "access": "public" for anyone.';
    END IF;

    IF p_metadata ? 'access' THEN
        access := p_metadata->>'access';
        IF jsonb_typeof(p_metadata->'access') IS DISTINCT FROM 'string'
           OR access NOT IN ('public', 'authenticated') THEN
            RAISE EXCEPTION 'Handler % declares access %, expected "public" or "authenticated"',
                p_label, p_metadata->'access'
                USING ERRCODE = 'invalid_parameter_value';
        END IF;
        RETURN;
    END IF;

    IF jsonb_typeof(p_metadata->'permissions') IS DISTINCT FROM 'array'
       OR jsonb_array_length(p_metadata->'permissions') = 0
       OR EXISTS (SELECT FROM jsonb_array_elements(p_metadata->'permissions') e
                  WHERE jsonb_typeof(e) IS DISTINCT FROM 'string') THEN
        RAISE EXCEPTION 'Handler % declares permissions %, expected a non-empty array of permission ids',
            p_label, p_metadata->'permissions'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    access := 'permission';
    permissions := ARRAY(SELECT DISTINCT e FROM jsonb_array_elements_text(p_metadata->'permissions') e ORDER BY e);

    SELECT string_agg(u, ', ' ORDER BY u) INTO v_unknown
    FROM unnest(permissions) u
    WHERE internal.is_platform_permission(u) IS NULL;
    IF v_unknown IS NOT NULL THEN
        RAISE EXCEPTION 'Handler % declares unknown permission(s): %', p_label, v_unknown
            USING ERRCODE = 'foreign_key_violation',
                  HINT = 'Permissions are a closed vocabulary: add it to "permissions" in data/authorization.json, '
                      || 'and to a role''s "grants" there, or no one can call the handler. Or fix the spelling.';
    END IF;
END $$;

CREATE OR REPLACE FUNCTION internal.store_handler_permissions(p_handler uuid, p_permissions text[])
RETURNS void
LANGUAGE sql AS $$
    DELETE FROM api.handler_permission
    WHERE handler_object_id = p_handler AND permission_id <> ALL (COALESCE(p_permissions, '{}'));
    INSERT INTO api.handler_permission (handler_object_id, permission_id)
    SELECT p_handler, unnest(COALESCE(p_permissions, '{}'))
    ON CONFLICT DO NOTHING;
$$;

-- ============================================================================
-- REST Handler Registration
-- ============================================================================

-- A schema references a named component as {"$ref": "#/components/schemas/<name>"}.
-- A reference that names no registered component would publish a document no
-- client can resolve, so registration refuses it.
CREATE OR REPLACE FUNCTION internal.dangling_refs(p_schemas jsonb, p_self text DEFAULT NULL)
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT string_agg(DISTINCT ref, ', ' ORDER BY ref)
    FROM jsonb_array_elements(p_schemas) AS s(schema)
    CROSS JOIN LATERAL jsonb_path_query(s.schema, 'strict $.**."$ref"') AS r(value)
    CROSS JOIN LATERAL (SELECT r.value #>> '{}' AS ref) x
    WHERE jsonb_typeof(s.schema) = 'object'
      AND NOT (x.ref IN ('#/components/schemas/Problem', '#/components/schemas/' || COALESCE(p_self, 'Problem'))
               OR EXISTS (SELECT FROM api.schema_component c
                          WHERE '#/components/schemas/' || c.name = x.ref));
$$;

CREATE OR REPLACE FUNCTION internal.refuse_dangling_refs(p_handler text, p_schemas jsonb, p_self text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_dangling text := internal.dangling_refs(p_schemas, p_self);
BEGIN
    IF v_dangling IS NOT NULL THEN
        RAISE EXCEPTION '%: $ref names no schema component: %', p_handler, v_dangling
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Register it first with api.create_or_replace_schema_component(name, schema), in a file sorted before the handler.';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION api.create_or_replace_schema_component(p_name text, p_schema jsonb, p_description text DEFAULT NULL)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM internal.refuse_dangling_refs('schema component ' || p_name, jsonb_build_array(p_schema), p_name);
    INSERT INTO api.schema_component (name, schema, description)
    VALUES (p_name, p_schema::api.json_schema, p_description)
    ON CONFLICT (name) DO UPDATE SET schema = EXCLUDED.schema, description = EXCLUDED.description
    WHERE (api.schema_component.schema, api.schema_component.description) IS DISTINCT FROM (EXCLUDED.schema, EXCLUDED.description);
END;
$$;

COMMENT ON FUNCTION api.create_or_replace_schema_component(text, jsonb, text) IS
    'Registers a named JSON Schema, published as components.schemas.<name> and referenced from a handler''s inputSchema, outputSchema or querySchema as {"$ref": "#/components/schemas/<name>"}. Problem is reserved for the framework''s RFC 9457 body.';

-- A readOnly route runs in a READ ONLY transaction, where the exchange log
-- cannot be written; logging it would silently log nothing.
CREATE OR REPLACE FUNCTION internal.refuse_logged_read_only(p_read_only boolean, p_auto_log boolean, p_name text)
RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF p_read_only AND p_auto_log THEN
        RAISE EXCEPTION 'Handler "%" is readOnly and logged: a READ ONLY transaction cannot write the exchange log', p_name
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Set "autoLog": false on a readOnly route.';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION api.create_or_replace_rest_handler(
    p_metadata jsonb,
    p_handler_body text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $func$
DECLARE
    v_id uuid;
    v_address_regexp text;
    v_http_method text;
    v_name text;
    v_title text;
    v_description text;
    v_accepts text[];
    v_produces text[];
    v_response_headers jsonb;
    v_auto_log boolean;
    v_access text;
    v_permissions text[];
    v_min_isolation text;
    v_read_only boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;

    v_input_schema api.json_schema;
    v_output_schema api.json_schema;

    v_path text;
    v_canonical_path text;
    v_existing_handler uuid;
    v_retired_key text;
    v_bad_segment text;
    v_path_params jsonb;
    v_probe text;
    v_overlap_msg text;
    v_query_schema api.json_schema;
    v_query_problem text;
    v_success_status jsonb;
    v_body_statuses int[];
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'REST handler metadata requires "id" (uuid)';
    END IF;

    SELECT k INTO v_retired_key
    FROM unnest(ARRAY['uri', 'example', 'version', 'query']) AS k
    WHERE p_metadata ? k
    LIMIT 1;
    IF v_retired_key IS NOT NULL THEN
        RAISE EXCEPTION 'REST handler %: the "%" key is not supported', COALESCE(p_metadata->>'name', v_id::text), v_retired_key
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = CASE v_retired_key
                      WHEN 'version' THEN 'Version in the path instead: /v2/projects.'
                      WHEN 'query' THEN 'Declare query parameters with "querySchema", a JSON Schema object.'
                      ELSE 'Declare the route with "path", a template such as /projects/{id}.' END;
    END IF;

    -- The query string is declared, not matched: ?a=1&b=2 and ?b=2&a=1 are one
    -- request, so a regex over the raw string would be order- and
    -- encoding-sensitive. The declaration drives enforcement and the spec.
    v_query_problem := CASE WHEN p_metadata ? 'querySchema'
                            THEN internal.query_schema_problem(p_metadata->'querySchema') END;
    IF v_query_problem IS NOT NULL THEN
        RAISE EXCEPTION 'REST handler %: %', COALESCE(p_metadata->>'name', v_id::text), v_query_problem
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    v_query_schema := (p_metadata->'querySchema')::api.json_schema;

    -- Matching is liberal at the funnel and exact at the identity: rest_invoke
    -- canonicalizes every valid spelling of a path before matching, and a
    -- route declares its identity once, as a template in canonical form. A
    -- parameter takes a whole segment; that keeps the ranking below complete.
    v_path := p_metadata->>'path';
    IF v_path IS NULL THEN
        RAISE EXCEPTION 'REST handler %: metadata requires "path" (e.g. /projects/{id})', COALESCE(p_metadata->>'name', v_id::text)
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    SELECT seg INTO v_bad_segment
    FROM unnest(string_to_array(ltrim(v_path, '/'), '/')) AS seg
    WHERE seg ~ '[{}]' AND seg !~ '^\{[A-Za-z_][A-Za-z0-9_]*(:[A-Za-z0-9_]*)?\}$'
    LIMIT 1;
    IF v_bad_segment IS NOT NULL THEN
        RAISE EXCEPTION 'REST handler %: path segment "%" mixes literal text and a parameter', COALESCE(p_metadata->>'name', v_id::text), v_bad_segment
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'A parameter takes a whole segment: /files/{name}, not /files/{name}.json.';
    END IF;

    SELECT m[1] INTO v_bad_segment
    FROM regexp_matches(v_path, '\{([^}:]+)', 'g') AS m
    GROUP BY m[1]
    HAVING count(*) > 1
    LIMIT 1;
    IF v_bad_segment IS NOT NULL THEN
        RAISE EXCEPTION 'REST handler %: path parameter "%" appears twice in %', COALESCE(p_metadata->>'name', v_id::text), v_bad_segment, v_path
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    BEGIN
        v_path_params := api.path_template_param_spec(v_path);
    EXCEPTION WHEN invalid_parameter_value THEN
        RAISE EXCEPTION 'REST handler %: %', COALESCE(p_metadata->>'name', v_id::text), SQLERRM
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Use {name}, {name:uuid} or {name:int}.';
    END;

    v_canonical_path := api.path_template_canonical(v_path);
    v_address_regexp := api.path_template_to_regex(v_canonical_path);
    -- A concrete path the route answers, each parameter a valid value of its
    -- type, so the conformance tests reach the handler and not a 400.
    SELECT string_agg(
               CASE WHEN p.param IS NULL THEN t.seg
                    WHEN p.param->>'type' = 'uuid' THEN '00000000-0000-4000-8000-000000000000'
                    WHEN p.param->>'type' = 'int' THEN '1'
                    ELSE 'p' END,
               '/' ORDER BY t.ord)
    INTO v_probe
    FROM unnest(string_to_array(v_canonical_path, '/')) WITH ORDINALITY AS t(seg, ord)
    LEFT JOIN jsonb_array_elements(v_path_params) AS p(param)
      ON t.seg = '{' || (p.param->>'name') || '}';

    IF api.canonical_path(v_probe) IS DISTINCT FROM v_probe OR v_path ~ '[?#]' THEN
        RAISE EXCEPTION 'REST handler %: path % is not in canonical form', COALESCE(p_metadata->>'name', v_id::text), v_path
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Start with /, no trailing or doubled slash, no . or .. segment, no query, no percent-encoded unreserved character.';
    END IF;

    v_http_method := COALESCE(p_metadata->>'httpMethod', '^(GET|POST|PUT|DELETE|PATCH)$');

    -- A bare 'GET' would match any method containing it.
    IF left(v_http_method, 1) IS DISTINCT FROM '^' OR right(v_http_method, 1) IS DISTINCT FROM '$' THEN
        RAISE EXCEPTION
            'Route % httpMethod must be anchored with ^ and $, got %',
            COALESCE(p_metadata->>'name', v_id::text), v_http_method
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    v_name := p_metadata->>'name';
    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    SELECT d.access, d.permissions INTO v_access, v_permissions
    FROM internal.declared_access(p_metadata, COALESCE(v_name, v_id::text)) d;
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);
    v_read_only := COALESCE((p_metadata->>'readOnly')::boolean, false);
    PERFORM internal.refuse_logged_read_only(v_read_only, v_auto_log, COALESCE(v_name, v_id::text));
    v_input_schema := (p_metadata->'inputSchema')::api.json_schema;
    v_output_schema := (p_metadata->'outputSchema')::api.json_schema;
    PERFORM internal.refuse_dangling_refs(COALESCE(v_name, v_id::text),
        jsonb_build_array(v_input_schema, v_output_schema, p_metadata->'querySchema'));
    v_success_status := COALESCE(p_metadata->'successStatus', '200');
    IF jsonb_typeof(v_success_status) IS DISTINCT FROM 'number'
       OR v_success_status::text !~ '^2[0-9][0-9]$' THEN
        RAISE EXCEPTION 'REST handler %: successStatus must be one 2xx status, got %', COALESCE(v_name, v_id::text), v_success_status
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    -- OpenAPI publishes only successStatus, so a 2xx the body spells out must
    -- be it. A status computed at run time is not checked.
    v_body_statuses := ARRAY(
        SELECT DISTINCT m[1]::int
        FROM regexp_matches(p_handler_body, '(?:_response\s*\(|\mreturn\s*\(|\mrow\s*\()\s*(2[0-9][0-9])\M', 'gi') AS m
        ORDER BY 1);
    IF cardinality(v_body_statuses) > 0 AND NOT v_success_status::text::int = ANY (v_body_statuses) THEN
        RAISE EXCEPTION 'REST handler %: the body answers % but successStatus is %',
            COALESCE(v_name, v_id::text), array_to_string(v_body_statuses, ', '), v_success_status
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = format('Declare ''successStatus'', %s in the metadata so OpenAPI publishes the status the handler sends.', v_body_statuses[1]);
    END IF;

    RAISE DEBUG 'register REST: id=%, path=%, method=%', v_id, v_path, v_http_method;

    v_accepts := CASE
        WHEN p_metadata->'accepts' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'accepts'))
        ELSE ARRAY['*/*']
    END;
    v_produces := CASE
        WHEN p_metadata->'produces' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'produces'))
        ELSE ARRAY['application/json']
    END;
    v_response_headers := COALESCE(p_metadata->'responseHeaders', '{}'::jsonb);

    IF v_name IS NOT NULL THEN
        PERFORM internal.validate_handler_name(v_name);
        v_function_name := v_name;
    ELSE
        v_function_name := 'rest_handler_' || replace(v_id::text, '-', '_');
    END IF;

    -- Two handlers sharing a name would both CREATE OR REPLACE the same
    -- api.<name>(api.rest_request), so the second silently replaces the first's
    -- body. Today that is caught only incidentally — api.handler.handler_func is
    -- UNIQUE and the identical signature yields the identical OID — which fails
    -- the deploy with a raw unique_violation and a 40-line INSERT dump, naming
    -- neither handler. RPC has had this pre-check since it was written; REST
    -- never got it. Say which name, and which handler already owns it.
    SELECT handler_object_id INTO v_existing_handler
    FROM api.rest_route
    WHERE route_name = v_name AND handler_object_id <> v_id;

    IF v_existing_handler IS NOT NULL THEN
        RAISE EXCEPTION 'REST route name "%" is already registered to handler %', v_name, v_existing_handler
            USING ERRCODE = 'unique_violation',
                  HINT = 'Handler names become function names (api.<name>): two handlers cannot share one. Rename this handler, or reuse the existing handler''s id to replace it.';
    END IF;

    -- Two routes that match one path differ in shape, and specificity orders
    -- them (a literal segment beats a parameter), or share one. Two of one
    -- shape with a common method cannot be ordered, so the second is refused.
    SELECT string_agg(
        format('%s has the shape of %s (%s)',
            COALESCE(v_name, v_id::text),
            COALESCE(r.route_name, r.handler_object_id::text),
            r.canonical_path),
        ', '
    )
    INTO v_overlap_msg
    FROM api.rest_route r
    WHERE r.handler_object_id <> v_id
      AND internal.route_shape(r.canonical_path) = internal.route_shape(v_canonical_path)
      AND EXISTS (
          SELECT 1
          FROM unnest(ARRAY['GET','POST','PUT','DELETE','PATCH','HEAD','OPTIONS']) AS m(method)
          WHERE m.method ~ v_http_method AND m.method ~ r.method_regexp
      );

    IF v_overlap_msg IS NOT NULL THEN
        RAISE EXCEPTION 'Route overlap detected: %', v_overlap_msg
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Two routes of one shape and a common method cannot be ranked. Give them different methods, or make a segment literal.';
    END IF;

    v_boundary := internal.random_dollar_quote_boundary(p_handler_body);

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.rest_request)
RETURNS api.http_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

    PERFORM internal.drop_renamed_handler(v_id, v_function_schema, v_function_name, 'api.rest_request');
    EXECUTE v_function_sql;

    SELECT oid INTO v_handler_oid
    FROM pg_proc
    WHERE pronamespace = v_function_schema::regnamespace
      AND proname = v_function_name
      AND pronargs = 1
      AND proargtypes[0] = 'api.rest_request'::regtype;

    IF v_handler_oid IS NULL THEN
        RAISE EXCEPTION 'Failed to create REST handler function';
    END IF;

    RAISE DEBUG 'register REST: Created function %.%', v_function_schema, v_function_name;

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.rest_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, access, min_transaction_isolation, read_only,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, 'rest', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_access, v_min_isolation, v_read_only,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_title, v_description, v_input_schema, v_output_schema
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        accepts = EXCLUDED.accepts,
        produces = EXCLUDED.produces,
        response_headers = EXCLUDED.response_headers,
        access = EXCLUDED.access,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
        read_only = EXCLUDED.read_only,
        handler_exec_sql = EXCLUDED.handler_exec_sql,
        handler_sql_submitted = EXCLUDED.handler_sql_submitted,
        handler_sql_canonical = EXCLUDED.handler_sql_canonical,
        def_hash = EXCLUDED.def_hash,
        returns_type = EXCLUDED.returns_type,
        returns_set = EXCLUDED.returns_set,
        volatility = EXCLUDED.volatility,
        parallel = EXCLUDED.parallel,
        leakproof = EXCLUDED.leakproof,
        security = EXCLUDED.security,
        language_name = EXCLUDED.language_name,
        owner_name = EXCLUDED.owner_name,
        title = EXCLUDED.title,
        description = EXCLUDED.description,
        input_json_schema = EXCLUDED.input_json_schema,
        output_json_schema = EXCLUDED.output_json_schema;

    INSERT INTO api.rest_route (handler_object_id, address_regexp, method_regexp, route_name, auto_log, canonical_path, path_params, probe_path, query_schema, success_status)
    VALUES (v_id, v_address_regexp, v_http_method, v_name, v_auto_log, v_canonical_path, v_path_params, v_probe, v_query_schema, v_success_status::text::int)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        path_params = EXCLUDED.path_params,
        probe_path = EXCLUDED.probe_path,
        query_schema = EXCLUDED.query_schema,
        success_status = EXCLUDED.success_status,
        address_regexp = EXCLUDED.address_regexp,
        method_regexp = EXCLUDED.method_regexp,
        route_name = EXCLUDED.route_name,
        auto_log = EXCLUDED.auto_log,
        canonical_path = EXCLUDED.canonical_path,
        -- The registration order of this deploy, so a fresh database and an
        -- existing one rank identically.
        sequence_number = DEFAULT;

    PERFORM internal.store_handler_permissions(v_id, v_permissions);

    RAISE DEBUG 'register REST: Registered route %', v_name;
END;
$func$;

COMMENT ON FUNCTION api.create_or_replace_rest_handler(jsonb, text) IS
    'Registers a REST handler: creates the handler function, snapshots pg_proc metadata, upserts into api.handler + api.rest_route. SECURITY DEFINER.';

-- ============================================================================
-- RPC Handler Registration
-- ============================================================================

CREATE OR REPLACE FUNCTION api.create_or_replace_rpc_handler(
    p_metadata jsonb,
    p_handler_body text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $func$
DECLARE
    v_id uuid;
    v_method_name text;
    v_title text;
    v_description text;
    v_accepts text[];
    v_produces text[];
    v_response_headers jsonb;
    v_auto_log boolean;
    v_access text;
    v_permissions text[];
    v_min_isolation text;
    v_read_only boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;

    v_existing_handler uuid;

    v_input_schema api.json_schema;
    v_output_schema api.json_schema;
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'RPC handler metadata requires "id" (uuid)';
    END IF;

    v_method_name := p_metadata->>'methodName';
    IF v_method_name IS NULL THEN
        RAISE EXCEPTION 'RPC handler metadata requires "methodName"';
    END IF;

    v_input_schema := (p_metadata->'inputSchema')::api.json_schema;
    v_output_schema := (p_metadata->'outputSchema')::api.json_schema;
    PERFORM internal.refuse_dangling_refs(v_method_name, jsonb_build_array(v_input_schema, v_output_schema));

    SELECT handler_object_id INTO v_existing_handler
    FROM api.rpc_route
    WHERE method_name = v_method_name AND handler_object_id != v_id;

    IF v_existing_handler IS NOT NULL THEN
        RAISE EXCEPTION 'RPC method name "%" already registered to handler %', v_method_name, v_existing_handler;
    END IF;

    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    SELECT d.access, d.permissions INTO v_access, v_permissions
    FROM internal.declared_access(p_metadata, v_method_name) d;
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);
    v_read_only := COALESCE((p_metadata->>'readOnly')::boolean, false);
    PERFORM internal.refuse_logged_read_only(v_read_only, v_auto_log, v_method_name);

    RAISE DEBUG 'register RPC: id=%, method=%', v_id, v_method_name;

    v_accepts := CASE
        WHEN p_metadata->'accepts' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'accepts'))
        ELSE ARRAY['application/json']
    END;
    v_produces := CASE
        WHEN p_metadata->'produces' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'produces'))
        ELSE ARRAY['application/json']
    END;
    v_response_headers := COALESCE(p_metadata->'responseHeaders', '{}'::jsonb);

    PERFORM internal.validate_handler_name(v_method_name);
    v_function_name := 'rpc_' || replace(replace(v_method_name, '.', '_'), '-', '_');
    v_boundary := internal.random_dollar_quote_boundary(p_handler_body);

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.rpc_request)
RETURNS api.http_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

    PERFORM internal.assert_function_name_free(v_id, v_function_schema, v_function_name, 'api.rpc_request', v_method_name);
    PERFORM internal.drop_renamed_handler(v_id, v_function_schema, v_function_name, 'api.rpc_request');
    EXECUTE v_function_sql;

    SELECT oid INTO v_handler_oid
    FROM pg_proc
    WHERE pronamespace = v_function_schema::regnamespace
      AND proname = v_function_name
      AND pronargs = 1
      AND proargtypes[0] = 'api.rpc_request'::regtype;

    IF v_handler_oid IS NULL THEN
        RAISE EXCEPTION 'Failed to create RPC handler function';
    END IF;

    RAISE DEBUG 'register RPC: Created function %.%', v_function_schema, v_function_name;

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.rpc_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, access, min_transaction_isolation, read_only,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, 'rpc', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_access, v_min_isolation, v_read_only,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_title, v_description, v_input_schema, v_output_schema
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        accepts = EXCLUDED.accepts,
        produces = EXCLUDED.produces,
        response_headers = EXCLUDED.response_headers,
        access = EXCLUDED.access,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
        read_only = EXCLUDED.read_only,
        handler_exec_sql = EXCLUDED.handler_exec_sql,
        handler_sql_submitted = EXCLUDED.handler_sql_submitted,
        handler_sql_canonical = EXCLUDED.handler_sql_canonical,
        def_hash = EXCLUDED.def_hash,
        returns_type = EXCLUDED.returns_type,
        returns_set = EXCLUDED.returns_set,
        volatility = EXCLUDED.volatility,
        parallel = EXCLUDED.parallel,
        leakproof = EXCLUDED.leakproof,
        security = EXCLUDED.security,
        language_name = EXCLUDED.language_name,
        owner_name = EXCLUDED.owner_name,
        title = EXCLUDED.title,
        description = EXCLUDED.description,
        input_json_schema = EXCLUDED.input_json_schema,
        output_json_schema = EXCLUDED.output_json_schema;

    INSERT INTO api.rpc_route (handler_object_id, method_name, auto_log)
    VALUES (v_id, v_method_name, v_auto_log)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        method_name = EXCLUDED.method_name,
        auto_log = EXCLUDED.auto_log;

    PERFORM internal.store_handler_permissions(v_id, v_permissions);

    RAISE DEBUG 'register RPC: Registered method %', v_method_name;
END;
$func$;

COMMENT ON FUNCTION api.create_or_replace_rpc_handler(jsonb, text) IS
    'Registers an RPC handler: creates the handler function, snapshots pg_proc metadata, upserts into api.handler + api.rpc_route. SECURITY DEFINER.';

-- ============================================================================
-- MCP Handler Registration
-- ============================================================================

-- What calling a tool does, declared once so an agent knows it before the
-- call: "effect" is read (changes nothing) or write (makes a change); a write
-- also declares "reversible", whether the change can be undone afterwards.
-- "openWorldHint" (default false) says the tool reaches outside this
-- database. A resource or a prompt declares none of them. Returns the effect
-- block tools/list publishes under _meta["pgmi/effect"]; NULL for a resource
-- or prompt.
CREATE OR REPLACE FUNCTION internal.mcp_tool_effect(p_metadata jsonb, p_type text, p_label text)
RETURNS jsonb
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    v_effect text := p_metadata->>'effect';
BEGIN
    IF p_type <> 'tool' THEN
        IF p_metadata ?| ARRAY['effect', 'reversible', 'openWorldHint', 'annotations'] THEN
            RAISE EXCEPTION 'MCP % "%" declares an effect or annotations, which only a tool declares', p_type, p_label
                USING ERRCODE = 'invalid_parameter_value',
                      HINT = 'Reading a resource or getting a prompt changes nothing: drop "effect", "reversible", "openWorldHint" and "annotations".';
        END IF;
        RETURN NULL;
    END IF;

    IF p_metadata ? 'annotations' THEN
        RAISE EXCEPTION 'MCP tool "%" declares "annotations", which are derived from "effect"', p_label
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'Drop "annotations"; declare "effect" (and "reversible", "openWorldHint").';
    END IF;
    IF jsonb_typeof(p_metadata->'effect') IS DISTINCT FROM 'string' OR v_effect NOT IN ('read', 'write') THEN
        RAISE EXCEPTION 'MCP tool "%" must declare "effect": "read" or "write", got %', p_label, COALESCE((p_metadata->'effect')::text, 'nothing')
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'read: the tool changes nothing; write: it makes the change, and declares "reversible". An agent reads it in tools/list before it calls the tool.';
    END IF;
    IF v_effect = 'read' AND p_metadata ? 'reversible' THEN
        RAISE EXCEPTION 'MCP tool "%" declares effect read, which takes no "reversible"', p_label
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_effect = 'write' AND jsonb_typeof(p_metadata->'reversible') IS DISTINCT FROM 'boolean' THEN
        RAISE EXCEPTION 'MCP tool "%" declares effect write and must declare "reversible": true or false, got %',
            p_label, COALESCE((p_metadata->'reversible')::text, 'nothing')
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'true when the change can be undone afterwards; false for a delete or anything else that cannot.';
    END IF;
    IF p_metadata ? 'openWorldHint' AND jsonb_typeof(p_metadata->'openWorldHint') IS DISTINCT FROM 'boolean' THEN
        RAISE EXCEPTION 'MCP tool "%": "openWorldHint" must be true or false, got %', p_label, p_metadata->'openWorldHint'
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    RETURN jsonb_build_object('effect', v_effect)
        || CASE WHEN v_effect = 'write' THEN jsonb_build_object('reversible', p_metadata->'reversible') ELSE '{}' END;
END;
$$;

COMMENT ON FUNCTION internal.mcp_tool_effect(jsonb, text, text) IS
    'A tool''s effect block from its metadata ({effect read} or {effect write, reversible}); raises invalid_parameter_value, failing the deploy, for a tool without a valid effect, hand-written annotations, or a resource or prompt that declares any of them. NULL for a resource or prompt.';

-- The MCP tool annotations, derived from the effect, never written by hand:
-- read-only and idempotent for a read, destructive for a write that cannot be
-- undone, open-world only when declared.
CREATE OR REPLACE FUNCTION internal.mcp_tool_annotations(p_effect jsonb, p_open_world boolean)
RETURNS jsonb
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT jsonb_build_object(
               'readOnlyHint', p_effect->>'effect' = 'read',
               'destructiveHint', p_effect->>'effect' = 'write' AND NOT (p_effect->>'reversible')::boolean,
               'idempotentHint', p_effect->>'effect' = 'read',
               'openWorldHint', COALESCE(p_open_world, false))
    WHERE p_effect IS NOT NULL
$$;

COMMENT ON FUNCTION internal.mcp_tool_annotations(jsonb, boolean) IS
    'A tool''s MCP annotations from its effect block: readOnlyHint and idempotentHint for a read, destructiveHint for a write that cannot be undone, openWorldHint as declared (default false). NULL for a resource or prompt.';

-- An MCP client resolves a tool's schema on its own: a $ref must point inside
-- the schema ("#" or "#/$defs/..."), never at #/components/... or another
-- document.
CREATE OR REPLACE FUNCTION internal.refuse_nonlocal_refs(p_handler text, p_schemas jsonb)
RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    v_refs text;
BEGIN
    SELECT string_agg(DISTINCT x.ref, ', ' ORDER BY x.ref) INTO v_refs
    FROM jsonb_array_elements(p_schemas) AS s(schema)
    CROSS JOIN LATERAL jsonb_path_query(s.schema, 'strict $.**."$ref"') AS r(value)
    CROSS JOIN LATERAL (SELECT r.value #>> '{}' AS ref) x
    WHERE jsonb_typeof(s.schema) = 'object'
      AND NOT (x.ref = '#' OR (x.ref LIKE '#/%' AND x.ref NOT LIKE '#/components/%'));
    IF v_refs IS NOT NULL THEN
        RAISE EXCEPTION 'MCP handler "%": $ref is not local to the schema: %', p_handler, v_refs
            USING ERRCODE = 'invalid_parameter_value',
                  HINT = 'An MCP client resolves a tool''s schema on its own. Put the definition under "$defs" and refer to it as "#/$defs/<name>".';
    END IF;
END;
$$;


CREATE OR REPLACE FUNCTION api.create_or_replace_mcp_handler(
    p_metadata jsonb,
    p_handler_body text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, common, pg_temp
AS $func$
DECLARE
    v_id uuid;
    v_type text;
    v_name text;
    v_title text;
    v_description text;
    v_input_schema api.json_schema;
    v_uri_template text;
    v_mime_type text;
    v_arguments jsonb;
    v_handler_type api.handler_type;
    v_access text;
    v_permissions text[];
    v_min_isolation text;
    v_read_only boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;

    v_output_schema api.json_schema;
    v_tags text[];
    v_existing_handler uuid;
    v_effect jsonb;
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'MCP handler metadata requires "id" (uuid)';
    END IF;

    v_type := p_metadata->>'type';
    IF v_type IS NULL OR v_type NOT IN ('tool', 'resource', 'prompt') THEN
        RAISE EXCEPTION 'MCP handler metadata requires "type" (tool, resource, or prompt)';
    END IF;

    v_name := p_metadata->>'name';
    IF v_name IS NULL THEN
        RAISE EXCEPTION 'MCP handler metadata requires "name"';
    END IF;

    -- api.mcp_route.mcp_name is UNIQUE, so a collision already fails the deploy —
    -- but as a raw unique_violation naming neither handler. Catch it here and say
    -- which name and which owner, like REST and RPC do.
    SELECT handler_object_id INTO v_existing_handler
    FROM api.mcp_route
    WHERE mcp_name = v_name AND handler_object_id <> v_id;

    IF v_existing_handler IS NOT NULL THEN
        RAISE EXCEPTION 'MCP name "%" is already registered to handler %', v_name, v_existing_handler
            USING ERRCODE = 'unique_violation',
                  HINT = 'MCP names are unique across tools, resources and prompts, and become function names. Rename this handler, or reuse the existing handler''s id to replace it.';
    END IF;

    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_input_schema := COALESCE(p_metadata->'inputSchema', '{"type":"object"}'::jsonb)::api.json_schema;
    v_output_schema := (p_metadata->'outputSchema')::api.json_schema;
    PERFORM internal.refuse_nonlocal_refs(v_name, jsonb_build_array(v_input_schema, v_output_schema));
    v_effect := internal.mcp_tool_effect(p_metadata, v_type, v_name);
    v_uri_template := p_metadata->>'uriTemplate';

    -- MCP requires a root {"type": "object"} for both schemas and a
    -- [{name, description?, required?}] list for prompt arguments; a
    -- validating client rejects the whole tools/list or prompts/list otherwise.
    IF v_type = 'tool' AND (v_input_schema::jsonb)->>'type' IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'MCP tool "%": inputSchema must be a JSON Schema with "type": "object"', v_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_output_schema IS NOT NULL AND (v_output_schema::jsonb)->>'type' IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'MCP handler "%": outputSchema must be a JSON Schema with "type": "object"', v_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_type = 'prompt' AND p_metadata->'arguments' IS NOT NULL AND (
        jsonb_typeof(p_metadata->'arguments') IS DISTINCT FROM 'array' OR EXISTS (
            SELECT 1 FROM jsonb_array_elements(p_metadata->'arguments') a
            WHERE jsonb_typeof(a) IS DISTINCT FROM 'object'
               OR jsonb_typeof(a->'name') IS DISTINCT FROM 'string'
               OR (a ? 'required' AND jsonb_typeof(a->'required') IS DISTINCT FROM 'boolean')))
    THEN
        RAISE EXCEPTION 'MCP prompt "%": arguments must be an array of {name, description?, required?}', v_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;

    IF v_type = 'resource' AND v_uri_template IS NULL THEN
        RAISE EXCEPTION 'MCP resource "%" requires "uriTemplate"', v_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    v_mime_type := COALESCE(p_metadata->>'mimeType', 'application/json');
    v_arguments := p_metadata->'arguments';
    SELECT d.access, d.permissions INTO v_access, v_permissions
    FROM internal.declared_access(p_metadata, v_name) d;
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);
    v_read_only := COALESCE((p_metadata->>'readOnly')::boolean, false);
    IF v_read_only IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'MCP handler "%" cannot be readOnly: every MCP exchange is logged, and a READ ONLY transaction cannot write the log', v_name
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    v_tags := CASE
        WHEN p_metadata->'tags' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'tags'))
        ELSE '{}'::text[]
    END;

    RAISE DEBUG 'register MCP: id=%, type=%, name=%', v_id, v_type, v_name;

    v_handler_type := ('mcp_' || v_type)::api.handler_type;

    PERFORM internal.validate_handler_name(v_name);
    v_function_name := 'mcp_' || v_type || '_' || replace(replace(v_name, '.', '_'), '-', '_');
    v_boundary := internal.random_dollar_quote_boundary(p_handler_body);

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.mcp_request)
RETURNS api.mcp_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

    PERFORM internal.assert_function_name_free(v_id, v_function_schema, v_function_name, 'api.mcp_request', v_name);
    PERFORM internal.drop_renamed_handler(v_id, v_function_schema, v_function_name, 'api.mcp_request');
    EXECUTE v_function_sql;

    SELECT oid INTO v_handler_oid
    FROM pg_proc
    WHERE pronamespace = v_function_schema::regnamespace
      AND proname = v_function_name
      AND pronargs = 1
      AND proargtypes[0] = 'api.mcp_request'::regtype;

    IF v_handler_oid IS NULL THEN
        RAISE EXCEPTION 'Failed to create MCP handler function';
    END IF;

    RAISE DEBUG 'register MCP: Created function %.%', v_function_schema, v_function_name;

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.mcp_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, access, min_transaction_isolation, read_only,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, v_handler_type, v_handler_oid::regprocedure, v_function_name,
        ARRAY['application/json'], ARRAY['application/json'], '{}'::jsonb, v_access, v_min_isolation, v_read_only,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_title, v_description, v_input_schema, v_output_schema
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_type = EXCLUDED.handler_type,
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        access = EXCLUDED.access,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
        read_only = EXCLUDED.read_only,
        handler_exec_sql = EXCLUDED.handler_exec_sql,
        handler_sql_submitted = EXCLUDED.handler_sql_submitted,
        handler_sql_canonical = EXCLUDED.handler_sql_canonical,
        def_hash = EXCLUDED.def_hash,
        returns_type = EXCLUDED.returns_type,
        returns_set = EXCLUDED.returns_set,
        volatility = EXCLUDED.volatility,
        parallel = EXCLUDED.parallel,
        leakproof = EXCLUDED.leakproof,
        security = EXCLUDED.security,
        language_name = EXCLUDED.language_name,
        owner_name = EXCLUDED.owner_name,
        title = EXCLUDED.title,
        description = EXCLUDED.description,
        input_json_schema = EXCLUDED.input_json_schema,
        output_json_schema = EXCLUDED.output_json_schema;

    INSERT INTO api.mcp_route (handler_object_id, mcp_type, mcp_name, input_schema, uri_template, mime_type, arguments, tags,
                               annotations, effect)
    VALUES (v_id, v_type, v_name, v_input_schema, v_uri_template, v_mime_type, v_arguments, v_tags,
            internal.mcp_tool_annotations(v_effect, (p_metadata->>'openWorldHint')::boolean), v_effect)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        mcp_type = EXCLUDED.mcp_type,
        mcp_name = EXCLUDED.mcp_name,
        input_schema = EXCLUDED.input_schema,
        uri_template = EXCLUDED.uri_template,
        mime_type = EXCLUDED.mime_type,
        arguments = EXCLUDED.arguments,
        tags = EXCLUDED.tags,
        annotations = EXCLUDED.annotations,
        effect = EXCLUDED.effect;

    PERFORM internal.store_handler_permissions(v_id, v_permissions);

    RAISE DEBUG 'register MCP: Registered % %', v_type, v_name;
END;
$func$;

COMMENT ON FUNCTION api.create_or_replace_mcp_handler(jsonb, text) IS
    'Registers an MCP handler (tool/resource/prompt): creates the handler function, snapshots pg_proc metadata, upserts into api.handler + api.mcp_route. A tool declares its "effect" (read, or write with "reversible"), from which its annotations are derived; a  must be local to the schema. SECURITY DEFINER.';

-- ============================================================================
-- Catalog Version — the cache-invalidation signal
-- ============================================================================
-- A digest of exactly the registry columns the OpenAPI document is built from,
-- so it changes when — and only when — the published contract changes. Served as
-- the /openapi.json ETag and stamped on every response as x-pgmi-catalog-version.
--
-- NOT api.handler.def_hash: that hashes the handler's function BODY, so it would
-- miss a route whose path, method, auth requirement, isolation floor, or schemas
-- changed without the body changing. The ETag would then claim "unchanged" about
-- a document that changed — worse than shipping no ETag at all.
--
-- NOT a digest of the rendered document. Nor is the digest recomputed per
-- request: every write to the registry moves api.catalog_state.generation (a
-- statement trigger), and each connection caches the digest of the generation
-- it last saw. The generation is transactional, so a cache can never outlive the
-- registry it describes.
--
-- The generator's version is digested too: a template upgrade that changes how
-- the document is derived changes the document without touching a row.
-- test_openapi_agreement.sql fails when api.openapi_document() reads a registry
-- column this digest omits.

CREATE OR REPLACE FUNCTION internal.openapi_generator_version()
RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT '2026-10-31.1';
$$;

CREATE OR REPLACE FUNCTION internal.catalog_digest()
RETURNS text
LANGUAGE sql STABLE PARALLEL SAFE
SET search_path = api, extensions, common, pg_temp
AS $catalog_digest$
    SELECT encode(extensions.digest(
               internal.openapi_generator_version() || E'\n'
               || COALESCE((
                   SELECT string_agg(c.fingerprint, E'\n' ORDER BY c.fingerprint)
                   FROM (
                       SELECT concat_ws('|',
                           h.object_id::text,
                           h.handler_function_name,
                           h.title,
                           h.description,
                           h.access,
                           (SELECT string_agg(hp.permission_id, ',' ORDER BY hp.permission_id)
                            FROM api.handler_permission hp WHERE hp.handler_object_id = h.object_id),
                           h.min_transaction_isolation,
                           h.read_only::text,
                           h.accepts::text,
                           h.produces::text,
                           h.input_json_schema::text,
                           h.output_json_schema::text,
                           r.address_regexp,
                           r.method_regexp,
                           r.canonical_path,
                           r.path_params::text,
                           r.query_schema::text,
                           r.success_status::text
                       ) AS fingerprint
                       FROM api.rest_route r
                       JOIN api.handler h ON h.object_id = r.handler_object_id
                       WHERE h.deleted_at IS NULL
                       UNION ALL
                       SELECT concat_ws('|', 'component', sc.name, sc.schema::text, sc.description)
                       FROM api.schema_component sc
                   ) c), ''),
               'sha256'), 'hex');
$catalog_digest$;

CREATE OR REPLACE FUNCTION api.catalog_version()
RETURNS text
LANGUAGE plpgsql STABLE
SET search_path = api, extensions, common, pg_temp
AS $catalog_version$
DECLARE
    v_generation text := (SELECT generation::text FROM api.catalog_state);
    v_cached text := current_setting('api.catalog_version_cache', true);
    v_digest text;
BEGIN
    IF split_part(v_cached, ':', 1) = v_generation THEN
        RETURN split_part(v_cached, ':', 2);
    END IF;
    v_digest := internal.catalog_digest();
    PERFORM set_config('api.catalog_version_cache', v_generation || ':' || v_digest, false);
    RETURN v_digest;
END;
$catalog_version$;

COMMENT ON FUNCTION api.catalog_version() IS
    'Digest of the registry rows the OpenAPI document is built from, plus the generator version; changes iff the published contract changes. Cached per connection per api.catalog_state.generation. Served as the /openapi.json ETag and stamped on every response as x-pgmi-catalog-version.';

CREATE OR REPLACE FUNCTION internal.bump_catalog_generation()
RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE api.catalog_state SET generation = generation + 1;
    RETURN NULL;
END;
$$;

CREATE OR REPLACE TRIGGER tr_handler_catalog_generation
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON api.handler
    FOR EACH STATEMENT EXECUTE FUNCTION internal.bump_catalog_generation();
CREATE OR REPLACE TRIGGER tr_rest_route_catalog_generation
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON api.rest_route
    FOR EACH STATEMENT EXECUTE FUNCTION internal.bump_catalog_generation();
CREATE OR REPLACE TRIGGER tr_handler_permission_catalog_generation
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON api.handler_permission
    FOR EACH STATEMENT EXECUTE FUNCTION internal.bump_catalog_generation();
CREATE OR REPLACE TRIGGER tr_schema_component_catalog_generation
    AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON api.schema_component
    FOR EACH STATEMENT EXECUTE FUNCTION internal.bump_catalog_generation();

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.create_or_replace_rest_handler() - REST handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_rpc_handler() - RPC handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_mcp_handler() - MCP handler registration';
    RAISE NOTICE '  ✓ api.catalog_version() - contract fingerprint (ETag / x-pgmi-catalog-version)';
END $$;
