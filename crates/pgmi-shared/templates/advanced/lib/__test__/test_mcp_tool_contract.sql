-- ============================================================================
-- Test: what a tool declares and how its arguments reach it
-- ============================================================================
-- A tool declares its effect, from which its MCP annotations are derived;
-- arguments are read leniently against the tool's own inputSchema; and no
-- tool answers malformed arguments with an internal error.
-- ============================================================================

CREATE FUNCTION pg_temp.tc_register(p_metadata jsonb, p_body text DEFAULT NULL)
RETURNS text LANGUAGE plpgsql AS $$
BEGIN
    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'f4500000-0000-4000-8000-0000000000aa', 'name', 'tc_probe',
                           'description', 'probe', 'access', 'public') || p_metadata,
        COALESCE(p_body, $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END;$b$));
    RETURN NULL;
EXCEPTION WHEN invalid_parameter_value THEN
    RETURN SQLERRM;
END $$;

-- ----------------------------------------------------------------------------
-- Effect and annotations
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_case  record;
    v_error text;
    v_tool  jsonb;
BEGIN
    RAISE NOTICE '-> Testing tool effects, annotations and schema references';

    PERFORM api.create_or_replace_mcp_handler(
        jsonb_build_object('id', 'f4500000-0000-4000-8000-0000000000ac', 'type', 'tool', 'name', 'tc_target',
                           'effect', 'write', 'reversible', true, 'description', 'target', 'access', 'public'),
        $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id); END;$b$);

    FOR v_case IN SELECT * FROM (VALUES
            ('{"type": "tool"}'::jsonb,                                              'must declare "effect"'),
            ('{"type": "tool", "effect": "proposal", "proposes": "tc_target", "reversible": true}', 'takes no "reversible"'),
            ('{"type": "tool", "effect": "proposal"}',                               'must declare "proposes"'),
            ('{"type": "tool", "effect": "proposal", "proposes": "tc_missing"}',     'not a registered write tool'),
            ('{"type": "tool", "effect": "proposal", "proposes": "echo_note"}',      'not a registered write tool'),
            ('{"type": "tool", "effect": "proposal", "proposes": "tc_probe"}',       'not a registered write tool'),
            ('{"type": "tool", "effect": "write", "reversible": true, "proposes": "tc_target"}', 'takes no "proposes"'),
            ('{"type": "prompt", "proposes": "tc_target"}',                          'only a tool declares'),
            ('{"type": "tool", "effect": "delete"}',                                 'must declare "effect"'),
            ('{"type": "tool", "effect": "write"}',                                  '"reversible"'),
            ('{"type": "tool", "effect": "write", "reversible": "yes"}',             '"reversible"'),
            ('{"type": "tool", "effect": "read", "reversible": true}',               'takes no "reversible"'),
            ('{"type": "tool", "effect": "read", "openWorldHint": "no"}',            '"openWorldHint"'),
            ('{"type": "tool", "effect": "read", "annotations": {"readOnlyHint": true}}', 'derived from "effect"'),
            ('{"type": "resource", "uriTemplate": "tc://x", "effect": "read"}',      'only a tool declares'),
            ('{"type": "prompt", "openWorldHint": false}',                           'only a tool declares'),
            ('{"type": "tool", "effect": "read", "inputSchema": {"type": "object", "properties": {"p": {"$ref": "#/components/schemas/Problem"}}}}', 'not local'),
            ('{"type": "tool", "effect": "read", "inputSchema": {"type": "object", "properties": {"p": {"$ref": "https://example.test/s.json"}}}}', 'not local'),
            ('{"type": "tool", "effect": "read", "outputSchema": {"type": "object", "properties": {"p": {"$ref": "other.json#/x"}}}}', 'not local')
        ) c(metadata, expected) LOOP
        v_error := pg_temp.tc_register(v_case.metadata);
        IF v_error IS NULL OR (strpos(v_error, v_case.expected) = 0) IS NOT FALSE OR (strpos(v_error, 'tc_probe') = 0) IS NOT FALSE THEN
            RAISE EXCEPTION 'TEST FAILED: % must be refused naming tc_probe and %, got %', v_case.metadata, v_case.expected, v_error;
        END IF;
    END LOOP;
    IF EXISTS (SELECT FROM api.mcp_route WHERE mcp_name = 'tc_probe') THEN
        RAISE EXCEPTION 'TEST FAILED: a refused registration left a route behind';
    END IF;

    FOR v_case IN SELECT * FROM (VALUES
            ('{"effect": "read"}'::jsonb,
             '{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}'::jsonb,
             '{"effect": "read"}'::jsonb),
            ('{"effect": "write", "reversible": true}',
             '{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}',
             '{"effect": "write", "reversible": true}'),
            ('{"effect": "write", "reversible": false, "openWorldHint": true}',
             '{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": true}',
             '{"effect": "write", "reversible": false}'),
            ('{"effect": "proposal", "proposes": "tc_target"}',
             '{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}',
             '{"effect": "proposal", "proposes": "tc_target"}')
        ) c(metadata, annotations, effect) LOOP
        v_error := pg_temp.tc_register(v_case.metadata || '{"type": "tool", "inputSchema": {"type": "object", "$defs": {"n": {"type": "integer"}}, "properties": {"n": {"$ref": "#/$defs/n"}}}}');
        IF v_error IS NOT NULL THEN
            RAISE EXCEPTION 'TEST FAILED: % refused: %', v_case.metadata, v_error;
        END IF;
        SELECT t INTO v_tool FROM jsonb_array_elements(api.mcp_list_tools()->'tools') t WHERE t->>'name' = 'tc_probe';
        IF v_tool->'annotations' IS DISTINCT FROM v_case.annotations OR v_tool->'_meta'->'pgmi/effect' IS DISTINCT FROM v_case.effect THEN
            RAISE EXCEPTION 'TEST FAILED: % published %', v_case.metadata, v_tool;
        END IF;
    END LOOP;

    -- A resource and a prompt carry neither.
    PERFORM pg_temp.tc_register('{"type": "prompt"}');
    IF EXISTS (SELECT FROM api.mcp_route WHERE mcp_name = 'tc_probe' AND (annotations IS NOT NULL OR effect IS NOT NULL)) THEN
        RAISE EXCEPTION 'TEST FAILED: a prompt kept a tool''s annotations or effect';
    END IF;

    RAISE DEBUG '  + effect required, annotations derived, non-local $ref refused';
END $$;

-- ----------------------------------------------------------------------------
-- Lenient reading: a lone string for an array of strings, an integral number
-- for an integer; everything else as sent
-- ----------------------------------------------------------------------------
DO $$
DECLARE
    v_schema jsonb := '{
        "type": "object",
        "$defs": {"count": {"type": "integer"}},
        "properties": {
            "tags":     {"type": "array", "items": {"type": "string"}},
            "states":   {"type": "array", "items": {"enum": ["open", "done"]}},
            "ids":      {"type": "array", "items": {"type": "integer"}},
            "limit":    {"$ref": "#/$defs/count"},
            "offset":   {"type": ["integer", "null"]},
            "weight":   {"type": "number"},
            "label":    {"type": ["string", "array"], "items": {"type": "string"}},
            "tasks":    {"type": "array", "items": {"type": "object", "properties": {
                            "priority": {"type": "integer"}, "tags": {"type": "array", "items": {"type": "string"}}}}}
        }}';
    v_case record;
BEGIN
    FOR v_case IN SELECT * FROM (VALUES
            ('{"tags": "urgent"}'::jsonb,                        '{"tags": ["urgent"]}'::jsonb),
            ('{"states": "open"}',                               '{"states": ["open"]}'),
            ('{"tags": ["urgent"]}',                             '{"tags": ["urgent"]}'),
            ('{"tags": 7}',                                      '{"tags": 7}'),
            ('{"ids": "1"}',                                     '{"ids": "1"}'),
            ('{"ids": [1.0, 2.5]}',                              '{"ids": [1, 2.5]}'),
            ('{"limit": 10.0}',                                  '{"limit": 10}'),
            ('{"limit": 2.5}',                                   '{"limit": 2.5}'),
            ('{"limit": "10"}',                                  '{"limit": "10"}'),
            ('{"offset": 0.0}',                                  '{"offset": 0}'),
            ('{"weight": 10.0}',                                 '{"weight": 10.0}'),
            ('{"label": "x"}',                                   '{"label": "x"}'),
            ('{"tasks": [{"priority": 3.0, "tags": "a", "note": 1.0}]}', '{"tasks": [{"priority": 3, "tags": ["a"], "note": 1.0}]}'),
            ('{"unknown": "x", "other": 1.0}',                   '{"unknown": "x", "other": 1.0}'),
            ('"x"',                                              '"x"')
        ) c(given, expected) LOOP
        IF internal.mcp_lenient_arguments(v_case.given, v_schema)::text IS DISTINCT FROM v_case.expected::text THEN
            RAISE EXCEPTION 'TEST FAILED: lenient % should be %, got %', v_case.given, v_case.expected,
                internal.mcp_lenient_arguments(v_case.given, v_schema);
        END IF;
    END LOOP;
    IF internal.mcp_lenient_arguments(NULL, v_schema) IS NOT NULL
       OR internal.mcp_lenient_arguments('{"a": 1.0}', NULL)::text IS DISTINCT FROM '{"a": 1.0}' THEN
        RAISE EXCEPTION 'TEST FAILED: NULL arguments stay NULL; no schema leaves them as sent';
    END IF;

    -- The dispatcher reads leniently before it checks the schema.
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f4500000-0000-4000-8000-0000000000ab', 'type', 'tool', 'name', 'tc_lenient', 'effect', 'read',
        'description', 'echo', 'access', 'public', 'inputSchema', v_schema),
        $b$BEGIN RETURN api.mcp_tool_result('[]', (request).request_id, false, (request).arguments); END;$b$);
    IF (api.mcp_call_tool('tc_lenient', '{"tags": "urgent", "limit": 5.0}', NULL, '1')).envelope->'result'->'structuredContent'
           IS DISTINCT FROM '{"tags": ["urgent"], "limit": 5}'::jsonb THEN
        RAISE EXCEPTION 'TEST FAILED: tools/call must hand the handler the leniently read arguments, got %',
            (api.mcp_call_tool('tc_lenient', '{"tags": "urgent", "limit": 5.0}', NULL, '1')).envelope;
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- No tool answers malformed arguments with an internal error. Every
-- registered tool is called with arguments generated from its own
-- inputSchema: a wrong JSON type for each property alone, for all of them at
-- once, and the same inside the first item of an array of objects, which the
-- dispatcher's schema check refuses; and every property at once at an edge
-- of its type (empty, negative, huge), which passes the check, since lengths
-- and ranges are the body's. Whatever passes reaches the body, which must
-- answer (an isError result is an answer), never raise.
-- ----------------------------------------------------------------------------
CREATE FUNCTION pg_temp.tc_resolved(p_schema jsonb, p_root jsonb)
RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN p_schema->>'$ref' LIKE '#/$defs/%' THEN p_root->'$defs'->substr(p_schema->>'$ref', 9) ELSE p_schema END
$$;

CREATE FUNCTION pg_temp.tc_takes(p_schema jsonb, p_type text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
    SELECT COALESCE(CASE jsonb_typeof(p_schema->'type')
                        WHEN 'string' THEN p_schema->>'type' = p_type
                        WHEN 'array' THEN p_schema->'type' ? p_type
                    END, false)
$$;

-- The wrong values for each property of p_props.
CREATE FUNCTION pg_temp.tc_wrong(p_root jsonb, p_props jsonb)
RETURNS TABLE (name text, value jsonb)
LANGUAGE sql STABLE AS $$
    SELECT p.key, w.value
    FROM jsonb_each(CASE WHEN jsonb_typeof(p_props) = 'object' THEN p_props ELSE '{}' END) p
    CROSS JOIN LATERAL (SELECT pg_temp.tc_resolved(p.value, p_root) AS s) r
    CROSS JOIN LATERAL (VALUES
        (pg_temp.tc_takes(r.s, 'array') AND NOT pg_temp.tc_takes(r.s, 'number'), '7'::jsonb),
        (pg_temp.tc_takes(r.s, 'array') AND NOT pg_temp.tc_takes(r.s, 'object'), '{"a": 1}'),
        (pg_temp.tc_takes(r.s, 'string') AND NOT pg_temp.tc_takes(r.s, 'object'), '{"a": 1}'),
        (pg_temp.tc_takes(r.s, 'integer') AND NOT pg_temp.tc_takes(r.s, 'number'), '2.5'),
        ((pg_temp.tc_takes(r.s, 'number') OR pg_temp.tc_takes(r.s, 'integer')) AND NOT pg_temp.tc_takes(r.s, 'string'), '"x"'),
        (pg_temp.tc_takes(r.s, 'object') AND NOT pg_temp.tc_takes(r.s, 'string'), '"x"'),
        (pg_temp.tc_takes(r.s, 'boolean') AND NOT pg_temp.tc_takes(r.s, 'string'), '"yes"')
    ) w(applies, value)
    WHERE w.applies
$$;

-- Every property of p_props at one edge of its declared type: the first enum
-- value where there is one, else empty or negative (p_huge false) or very
-- large (p_huge true).
CREATE FUNCTION pg_temp.tc_edge(p_root jsonb, p_props jsonb, p_huge boolean)
RETURNS jsonb
LANGUAGE sql STABLE AS $$
    SELECT jsonb_object_agg(p.key, CASE
        WHEN jsonb_typeof(r.s->'enum') = 'array' THEN r.s->'enum'->0
        WHEN pg_temp.tc_takes(r.s, 'string')  THEN to_jsonb(CASE WHEN p_huge THEN repeat('x', 10000) ELSE '' END)
        WHEN pg_temp.tc_takes(r.s, 'integer') THEN CASE WHEN p_huge THEN '9223372036854775807' ELSE '-1' END::jsonb
        WHEN pg_temp.tc_takes(r.s, 'number')  THEN CASE WHEN p_huge THEN '1e300' ELSE '-1' END::jsonb
        WHEN pg_temp.tc_takes(r.s, 'boolean') THEN to_jsonb(p_huge)
        WHEN pg_temp.tc_takes(r.s, 'array')   THEN '[]'::jsonb
        ELSE '{}'::jsonb END)
    FROM jsonb_each(CASE WHEN jsonb_typeof(p_props) = 'object' THEN p_props ELSE '{}' END) p
    CROSS JOIN LATERAL (SELECT pg_temp.tc_resolved(p.value, p_root) AS s) r
$$;

CREATE FUNCTION pg_temp.tc_cases()
RETURNS TABLE (tool text, exec_sql text, root jsonb, arguments jsonb)
LANGUAGE sql STABLE AS $$
    WITH tool AS (
        SELECT r.mcp_name, h.handler_exec_sql, r.input_schema AS root
        FROM api.mcp_route r
        JOIN api.handler h ON h.object_id = r.handler_object_id AND h.deleted_at IS NULL
        WHERE r.mcp_type = 'tool'
    )
    SELECT t.mcp_name, t.handler_exec_sql, t.root, jsonb_build_object(w.name, w.value)
    FROM tool t CROSS JOIN LATERAL pg_temp.tc_wrong(t.root, t.root->'properties') w
    UNION ALL
    SELECT t.mcp_name, t.handler_exec_sql, t.root, a.arguments
    FROM tool t
    CROSS JOIN LATERAL (SELECT jsonb_object_agg(w.name, w.value) AS arguments
                        FROM pg_temp.tc_wrong(t.root, t.root->'properties') w) a
    WHERE a.arguments IS NOT NULL
    UNION ALL
    SELECT t.mcp_name, t.handler_exec_sql, t.root, jsonb_build_object(p.key, jsonb_build_array(jsonb_build_object(w.name, w.value)))
    FROM tool t
    CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(t.root->'properties') = 'object' THEN t.root->'properties' ELSE '{}' END) p
    CROSS JOIN LATERAL (SELECT pg_temp.tc_resolved(pg_temp.tc_resolved(p.value, t.root)->'items', t.root) AS items) i
    CROSS JOIN LATERAL pg_temp.tc_wrong(t.root, i.items->'properties') w
    WHERE pg_temp.tc_takes(pg_temp.tc_resolved(p.value, t.root), 'array') AND pg_temp.tc_takes(i.items, 'object')
    UNION ALL
    SELECT t.mcp_name, t.handler_exec_sql, t.root, pg_temp.tc_edge(t.root, t.root->'properties', h.huge)
    FROM tool t CROSS JOIN (VALUES (false), (true)) h(huge)
    WHERE jsonb_typeof(t.root->'properties') = 'object'
$$;

DO $$
DECLARE
    v_case      record;
    v_arguments jsonb;
    v_response  api.mcp_response;
    v_calls     int := 0;
    v_bodies    int := 0;
    v_fail      text[] := '{}';
BEGIN
    -- A tool that reads a list of tasks the way the recipe says: it checks
    -- each item's types before it uses them.
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'f4500000-0000-4000-8000-0000000000ac', 'type', 'tool', 'name', 'tc_plan_tasks',
        'effect', 'write', 'reversible', true, 'description', 'plan tasks', 'access', 'public',
        'inputSchema', '{"type": "object", "required": ["tasks"],
                         "$defs": {"task": {"type": "object", "required": ["title"],
                                            "properties": {"title": {"type": "string"}, "priority": {"type": "integer"},
                                                           "done": {"type": "boolean"}}}},
                         "properties": {"project": {"type": "string"}, "limit": {"type": "integer"},
                                        "tasks": {"type": "array", "items": {"$ref": "#/$defs/task"}}}}'::jsonb),
        $b$
DECLARE
    v_problems jsonb;
BEGIN
    SELECT jsonb_agg(jsonb_build_object('name', format('tasks[%s].%s', t.n - 1, f.name), 'reason', 'invalid') ORDER BY t.n, f.name)
    INTO v_problems
    FROM jsonb_array_elements((request).arguments->'tasks') WITH ORDINALITY t(item, n)
    CROSS JOIN LATERAL (VALUES ('title', 'string'), ('priority', 'number'), ('done', 'boolean')) f(name, type)
    WHERE jsonb_typeof(t.item) IS DISTINCT FROM 'object'
       OR (t.item ? f.name AND jsonb_typeof(t.item->f.name) IS DISTINCT FROM f.type)
       OR (f.name = 'priority' AND CASE WHEN jsonb_typeof(t.item->'priority') = 'number'
                                        THEN (t.item->>'priority')::numeric IS DISTINCT FROM trunc((t.item->>'priority')::numeric)
                                        ELSE false END);
    IF v_problems IS NOT NULL THEN
        RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text(jsonb_build_object(
            'code', 'pgmi.invalid_arguments', 'invalidParams', v_problems)::text)), (request).request_id, true);
    END IF;
    RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text(format('%s tasks planned',
        jsonb_array_length((request).arguments->'tasks')))), (request).request_id);
END;
        $b$);

    FOR v_case IN SELECT * FROM pg_temp.tc_cases() ORDER BY tool, arguments::text LOOP
        v_calls := v_calls + 1;
        v_arguments := internal.mcp_lenient_arguments(v_case.arguments, v_case.root);
        CONTINUE WHEN internal.json_schema_errors(v_case.root, v_arguments) IS NOT NULL;
        v_bodies := v_bodies + 1;
        BEGIN
            EXECUTE v_case.exec_sql INTO v_response USING ROW(v_arguments, NULL, NULL, '"tc"'::jsonb)::api.mcp_request;
        EXCEPTION WHEN OTHERS THEN
            v_fail := v_fail || format('%s %s: sqlstate=%s %s', v_case.tool, v_case.arguments, SQLSTATE, SQLERRM);
        END;
    END LOOP;

    IF (v_bodies = 0) IS NOT FALSE OR NOT EXISTS (SELECT FROM pg_temp.tc_cases() WHERE tool = 'tc_plan_tasks') THEN
        RAISE EXCEPTION 'TEST FAILED: the case generator reached no tool body (% calls)', v_calls;
    END IF;
    IF cardinality(v_fail) > 0 THEN
        RAISE EXCEPTION 'TEST FAILED: % of % malformed calls raised instead of answering:%',
            cardinality(v_fail), v_calls, E'\n' || array_to_string(v_fail, E'\n');
    END IF;
    RAISE DEBUG '  + % malformed calls, % reached a tool body, none raised', v_calls, v_bodies;
END $$;
