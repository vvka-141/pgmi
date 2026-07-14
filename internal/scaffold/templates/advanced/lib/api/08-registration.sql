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
-- REST Handler Registration
-- ============================================================================

CREATE OR REPLACE FUNCTION api.create_or_replace_rest_handler(
    p_metadata jsonb,
    p_handler_body text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $func$
DECLARE
    v_id uuid;
    v_uri text;
    v_http_method text;
    v_version text;
    v_name text;
    v_title text;
    v_description text;
    v_accepts text[];
    v_produces text[];
    v_response_headers jsonb;
    v_auto_log boolean;
    v_requires_auth boolean;
    v_min_isolation text;

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

    v_path_params text[];
    v_group_count bigint;
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'REST handler metadata requires "id" (uuid)';
    END IF;

    v_uri := p_metadata->>'uri';
    IF v_uri IS NULL THEN
        RAISE EXCEPTION 'REST handler metadata requires "uri" (regex pattern)';
    END IF;

    -- pathParams names the uri's capture groups for the OpenAPI document. Reject
    -- a miscount at deploy time: a silently wrong name would surface only as a
    -- mis-bound client, long after the deploy that caused it.
    v_path_params := CASE
        WHEN p_metadata->'pathParams' IS NOT NULL
        THEN ARRAY(SELECT jsonb_array_elements_text(p_metadata->'pathParams'))
        ELSE '{}'::text[]
    END;

    IF cardinality(v_path_params) > 0 THEN
        SELECT count(*) INTO v_group_count
        FROM api.route_path_tokens(v_uri)
        WHERE group_index IS NOT NULL;

        IF cardinality(v_path_params) != v_group_count THEN
            RAISE EXCEPTION
                'Route % declares % pathParams but its uri has % capture group(s): %',
                COALESCE(p_metadata->>'name', v_id::text),
                cardinality(v_path_params), v_group_count, v_uri
                USING ERRCODE = 'invalid_parameter_value';
        END IF;

        IF EXISTS (
            SELECT 1 FROM unnest(v_path_params) AS n
            WHERE n !~ '^[A-Za-z_][A-Za-z0-9_-]*$'
        ) THEN
            RAISE EXCEPTION 'pathParams must be OpenAPI parameter names matching ^[A-Za-z_][A-Za-z0-9_-]*$, got %',
                v_path_params
                USING ERRCODE = 'invalid_parameter_value';
        END IF;
    END IF;

    v_http_method := COALESCE(p_metadata->>'httpMethod', '^(GET|POST|PUT|DELETE|PATCH)$');
    v_version := COALESCE(p_metadata->>'version', '.*');
    v_name := p_metadata->>'name';
    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);
    v_input_schema := (p_metadata->'inputSchema')::api.json_schema;
    v_output_schema := (p_metadata->'outputSchema')::api.json_schema;

    RAISE DEBUG 'register REST: id=%, uri=%, method=%', v_id, v_uri, v_http_method;

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
        accepts, produces, response_headers, requires_auth, min_transaction_isolation,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, 'rest', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_requires_auth, v_min_isolation,
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
        requires_auth = EXCLUDED.requires_auth,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
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

    INSERT INTO api.rest_route (handler_object_id, address_regexp, method_regexp, version_regexp, route_name, auto_log, path_param_names)
    VALUES (v_id, v_uri, v_http_method, v_version, v_name, v_auto_log, v_path_params)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        address_regexp = EXCLUDED.address_regexp,
        method_regexp = EXCLUDED.method_regexp,
        version_regexp = EXCLUDED.version_regexp,
        route_name = EXCLUDED.route_name,
        auto_log = EXCLUDED.auto_log,
        path_param_names = EXCLUDED.path_param_names;

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
SET search_path = api, internal, extensions, pg_temp
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
    v_requires_auth boolean;
    v_min_isolation text;

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

    SELECT handler_object_id INTO v_existing_handler
    FROM api.rpc_route
    WHERE method_name = v_method_name AND handler_object_id != v_id;

    IF v_existing_handler IS NOT NULL THEN
        RAISE EXCEPTION 'RPC method name "%" already registered to handler %', v_method_name, v_existing_handler;
    END IF;

    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);

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
        accepts, produces, response_headers, requires_auth, min_transaction_isolation,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, 'rpc', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_requires_auth, v_min_isolation,
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
        requires_auth = EXCLUDED.requires_auth,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
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

    RAISE DEBUG 'register RPC: Registered method %', v_method_name;
END;
$func$;

COMMENT ON FUNCTION api.create_or_replace_rpc_handler(jsonb, text) IS
    'Registers an RPC handler: creates the handler function, snapshots pg_proc metadata, upserts into api.handler + api.rpc_route. SECURITY DEFINER.';

-- ============================================================================
-- MCP Handler Registration
-- ============================================================================

CREATE OR REPLACE FUNCTION api.create_or_replace_mcp_handler(
    p_metadata jsonb,
    p_handler_body text
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = api, internal, extensions, pg_temp
AS $func$
DECLARE
    v_id uuid;
    v_type text;
    v_name text;
    v_title text;
    v_description text;
    v_input_schema jsonb;
    v_uri_template text;
    v_mime_type text;
    v_arguments jsonb;
    v_handler_type api.handler_type;
    v_requires_auth boolean;
    v_min_isolation text;

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

    v_title := p_metadata->>'title';
    v_description := p_metadata->>'description';
    v_input_schema := p_metadata->'inputSchema';
    v_output_schema := (p_metadata->'outputSchema')::api.json_schema;
    v_uri_template := p_metadata->>'uriTemplate';
    v_mime_type := COALESCE(p_metadata->>'mimeType', 'application/json');
    v_arguments := p_metadata->'arguments';
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);
    v_min_isolation := internal.normalize_transaction_isolation(p_metadata->>'minTransactionIsolation', true);
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
        accepts, produces, response_headers, requires_auth, min_transaction_isolation,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        title, description, input_json_schema, output_json_schema
    ) VALUES (
        v_id, v_handler_type, v_handler_oid::regprocedure, v_function_name,
        ARRAY['application/json'], ARRAY['application/json'], '{}'::jsonb, v_requires_auth, v_min_isolation,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_title, v_description, v_input_schema::api.json_schema, v_output_schema
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_type = EXCLUDED.handler_type,
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        requires_auth = EXCLUDED.requires_auth,
        min_transaction_isolation = EXCLUDED.min_transaction_isolation,
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

    INSERT INTO api.mcp_route (handler_object_id, mcp_type, mcp_name, input_schema, uri_template, mime_type, arguments, tags)
    VALUES (v_id, v_type, v_name, v_input_schema, v_uri_template, v_mime_type, v_arguments, v_tags)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        mcp_type = EXCLUDED.mcp_type,
        mcp_name = EXCLUDED.mcp_name,
        input_schema = EXCLUDED.input_schema,
        uri_template = EXCLUDED.uri_template,
        mime_type = EXCLUDED.mime_type,
        arguments = EXCLUDED.arguments,
        tags = EXCLUDED.tags;

    RAISE DEBUG 'register MCP: Registered % %', v_type, v_name;
END;
$func$;

COMMENT ON FUNCTION api.create_or_replace_mcp_handler(jsonb, text) IS
    'Registers an MCP handler (tool/resource/prompt): creates the handler function, snapshots pg_proc metadata, upserts into api.handler + api.mcp_route. SECURITY DEFINER.';

-- ============================================================================
-- Catalog Version — the cache-invalidation signal
-- ============================================================================
-- A digest of exactly the registry columns the OpenAPI document is built from,
-- so it changes when — and only when — the published contract changes. Served as
-- the /openapi.json ETag and stamped on every response as x-pgmi-catalog-version.
--
-- NOT api.handler.def_hash: that hashes the handler's function BODY, so it would
-- miss a route whose uri, method, auth requirement, isolation floor, or schemas
-- changed without the body changing. The ETag would then claim "unchanged" about
-- a document that changed — worse than shipping no ETag at all.
--
-- NOT a digest of the rendered document: this is stamped on every response, and
-- rebuilding the whole spec per request is precisely the cost this exists to
-- avoid. Digesting the source rows is cheap and exactly as sensitive.
--
-- Lives here, not in 11-openapi.sql, because internal.finalize_response_headers
-- (004/009) calls it and PostgreSQL validates SQL function bodies at creation.

CREATE OR REPLACE FUNCTION api.catalog_version()
RETURNS text
LANGUAGE sql STABLE PARALLEL SAFE
SET search_path = api, extensions, pg_temp
AS $catalog_version$
    SELECT COALESCE(
        encode(
            extensions.digest(
                string_agg(c.row_fingerprint, E'\n' ORDER BY c.row_fingerprint),
                'sha256'),
            'hex'),
        'empty')
    FROM (
        SELECT concat_ws('|',
            h.object_id::text,
            h.handler_function_name,
            h.title,
            h.description,
            h.requires_auth::text,
            h.min_transaction_isolation,
            h.accepts::text,
            h.produces::text,
            h.input_json_schema::text,
            h.output_json_schema::text,
            r.address_regexp,
            r.method_regexp,
            r.path_param_names::text
        ) AS row_fingerprint
        FROM api.rest_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id
        WHERE h.deleted_at IS NULL
    ) c;
$catalog_version$;

COMMENT ON FUNCTION api.catalog_version() IS
    'Digest of the registry columns the OpenAPI document is built from. Changes iff the published contract changes. Served as the /openapi.json ETag and stamped on every response as x-pgmi-catalog-version, so a client learns its cached contract went stale without polling the spec.';

DO $$
DECLARE
    v_api_role TEXT := pg_temp.deployment_setting('database_api_role');
    v_admin_role TEXT := pg_temp.deployment_setting('database_admin_role');
    v_customer_role TEXT := pg_temp.deployment_setting('database_customer_role');
BEGIN
    EXECUTE format('GRANT EXECUTE ON FUNCTION api.catalog_version() TO %I, %I, %I',
        v_admin_role, v_api_role, v_customer_role);
END $$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.create_or_replace_rest_handler() - REST handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_rpc_handler() - RPC handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_mcp_handler() - MCP handler registration';
    RAISE NOTICE '  ✓ api.catalog_version() - contract fingerprint (ETag / x-pgmi-catalog-version)';
END $$;
