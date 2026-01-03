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
    v_description text;
    v_accepts text[];
    v_produces text[];
    v_response_headers jsonb;
    v_auto_log boolean;
    v_requires_auth boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'REST handler metadata requires "id" (uuid)';
    END IF;

    v_uri := p_metadata->>'uri';
    IF v_uri IS NULL THEN
        RAISE EXCEPTION 'REST handler metadata requires "uri" (regex pattern)';
    END IF;

    v_http_method := COALESCE(p_metadata->>'httpMethod', '^(GET|POST|PUT|DELETE|PATCH)$');
    v_version := COALESCE(p_metadata->>'version', '.*');
    v_name := p_metadata->>'name';
    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);

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

    v_function_name := COALESCE(v_name, 'rest_handler_' || replace(v_id::text, '-', '_'));
    v_boundary := 'hb_' || replace(v_id::text, '-', '');

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.rest_request)
RETURNS api.http_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

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

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.rest_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, requires_auth,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        description
    ) VALUES (
        v_id, 'rest', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_requires_auth,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_description
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        accepts = EXCLUDED.accepts,
        produces = EXCLUDED.produces,
        response_headers = EXCLUDED.response_headers,
        requires_auth = EXCLUDED.requires_auth,
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
        description = EXCLUDED.description;

    INSERT INTO api.rest_route (handler_object_id, address_regexp, method_regexp, version_regexp, route_name, auto_log)
    VALUES (v_id, v_uri, v_http_method, v_version, v_name, v_auto_log)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        address_regexp = EXCLUDED.address_regexp,
        method_regexp = EXCLUDED.method_regexp,
        version_regexp = EXCLUDED.version_regexp,
        route_name = EXCLUDED.route_name,
        auto_log = EXCLUDED.auto_log;
END;
$func$;

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
    v_description text;
    v_accepts text[];
    v_produces text[];
    v_response_headers jsonb;
    v_auto_log boolean;
    v_requires_auth boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;

    v_existing_handler uuid;
BEGIN
    v_id := (p_metadata->>'id')::uuid;
    IF v_id IS NULL THEN
        RAISE EXCEPTION 'RPC handler metadata requires "id" (uuid)';
    END IF;

    v_method_name := p_metadata->>'methodName';
    IF v_method_name IS NULL THEN
        RAISE EXCEPTION 'RPC handler metadata requires "methodName"';
    END IF;

    SELECT handler_object_id INTO v_existing_handler
    FROM api.rpc_route
    WHERE method_name = v_method_name AND handler_object_id != v_id;

    IF v_existing_handler IS NOT NULL THEN
        RAISE EXCEPTION 'RPC method name "%" already registered to handler %', v_method_name, v_existing_handler;
    END IF;

    v_description := p_metadata->>'description';
    v_auto_log := COALESCE((p_metadata->>'autoLog')::boolean, true);
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);

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

    v_function_name := 'rpc_' || replace(replace(v_method_name, '.', '_'), '-', '_');
    v_boundary := 'hb_' || replace(v_id::text, '-', '');

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.rpc_request)
RETURNS api.http_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

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

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.rpc_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, requires_auth,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        description
    ) VALUES (
        v_id, 'rpc', v_handler_oid::regprocedure, v_function_name,
        v_accepts, v_produces, v_response_headers, v_requires_auth,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_description
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        accepts = EXCLUDED.accepts,
        produces = EXCLUDED.produces,
        response_headers = EXCLUDED.response_headers,
        requires_auth = EXCLUDED.requires_auth,
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
        description = EXCLUDED.description;

    INSERT INTO api.rpc_route (handler_object_id, method_name, auto_log)
    VALUES (v_id, v_method_name, v_auto_log)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        method_name = EXCLUDED.method_name,
        auto_log = EXCLUDED.auto_log;
END;
$func$;

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
    v_description text;
    v_input_schema jsonb;
    v_uri_template text;
    v_mime_type text;
    v_arguments jsonb;
    v_handler_type api.handler_type;
    v_requires_auth boolean;

    v_function_schema text := 'api';
    v_function_name text;
    v_function_sql text;
    v_boundary text;

    v_handler_oid oid;
    v_snapshot record;
    v_handler_exec_sql text;
    v_def_hash bytea;
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

    v_description := p_metadata->>'description';
    v_input_schema := p_metadata->'inputSchema';
    v_uri_template := p_metadata->>'uriTemplate';
    v_mime_type := COALESCE(p_metadata->>'mimeType', 'application/json');
    v_arguments := p_metadata->'arguments';
    v_requires_auth := COALESCE((p_metadata->>'requiresAuth')::boolean, true);

    v_handler_type := ('mcp_' || v_type)::api.handler_type;

    v_function_name := 'mcp_' || v_type || '_' || replace(replace(v_name, '.', '_'), '-', '_');
    v_boundary := 'hb_' || replace(v_id::text, '-', '');

    v_function_sql := format(
        $sql$CREATE OR REPLACE FUNCTION %I.%I(request api.mcp_request)
RETURNS api.mcp_response AS $%s$
%s
$%s$ LANGUAGE plpgsql$sql$,
        v_function_schema, v_function_name, v_boundary, p_handler_body, v_boundary
    );

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

    SELECT * INTO v_snapshot FROM internal.capture_handler_proc_snapshot(v_handler_oid);

    v_handler_exec_sql := format('SELECT * FROM %I.%I($1::api.mcp_request)', v_function_schema, v_function_name);
    v_def_hash := extensions.digest(convert_to(v_snapshot.handler_canonical, 'UTF8'), 'sha256');

    INSERT INTO api.handler (
        object_id, handler_type, handler_func, handler_function_name,
        accepts, produces, response_headers, requires_auth,
        handler_exec_sql, handler_sql_submitted, handler_sql_canonical, def_hash,
        returns_type, returns_set, volatility, parallel, leakproof, security, language_name, owner_name,
        description
    ) VALUES (
        v_id, v_handler_type, v_handler_oid::regprocedure, v_function_name,
        ARRAY['application/json'], ARRAY['application/json'], '{}'::jsonb, v_requires_auth,
        v_handler_exec_sql, v_function_sql, v_snapshot.handler_canonical, v_def_hash,
        v_snapshot.returns_type, v_snapshot.returns_set, v_snapshot.volatility,
        v_snapshot.parallel, v_snapshot.leakproof, v_snapshot.security,
        v_snapshot.language_name, v_snapshot.owner_name,
        v_description
    )
    ON CONFLICT (object_id) DO UPDATE SET
        handler_type = EXCLUDED.handler_type,
        handler_func = EXCLUDED.handler_func,
        handler_function_name = EXCLUDED.handler_function_name,
        requires_auth = EXCLUDED.requires_auth,
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
        description = EXCLUDED.description;

    INSERT INTO api.mcp_route (handler_object_id, mcp_type, mcp_name, input_schema, uri_template, mime_type, arguments)
    VALUES (v_id, v_type, v_name, v_input_schema, v_uri_template, v_mime_type, v_arguments)
    ON CONFLICT (handler_object_id) DO UPDATE SET
        mcp_type = EXCLUDED.mcp_type,
        mcp_name = EXCLUDED.mcp_name,
        input_schema = EXCLUDED.input_schema,
        uri_template = EXCLUDED.uri_template,
        mime_type = EXCLUDED.mime_type,
        arguments = EXCLUDED.arguments;
END;
$func$;

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.create_or_replace_rest_handler() - REST handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_rpc_handler() - RPC handler registration';
    RAISE NOTICE '  ✓ api.create_or_replace_mcp_handler() - MCP handler registration';
END $$;
