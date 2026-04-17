/*
<pgmi-meta
    id="a7f01000-0001-4000-8000-000000000001"
    idempotent="true">
  <description>
    API foundation types: handler_type enum, protocol-specific request/response types
  </description>
  <sortKeys>
    <key>004/001</key>
  </sortKeys>
</pgmi-meta>
*/

DO $$ BEGIN RAISE NOTICE '→ Installing API foundation types'; END $$;

-- ============================================================================
-- Handler Type Enum
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'handler_type' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.handler_type AS ENUM (
            'rest',
            'rpc',
            'mcp_tool',
            'mcp_resource',
            'mcp_prompt'
        );
    END IF;
END $$;

-- ============================================================================
-- REST Request Type
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'rest_request' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.rest_request AS (
            method text,
            url text,
            headers extensions.hstore,
            content bytea
        );
    END IF;
END $$;

-- ============================================================================
-- RPC Request Type
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'rpc_request' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.rpc_request AS (
            route_id uuid,
            headers extensions.hstore,
            content bytea
        );
    END IF;
END $$;

-- ============================================================================
-- MCP Request Type
-- ============================================================================
-- request_id is jsonb to preserve JSON-RPC 2.0 id semantics (string, integer,
-- or null). Redeploy guard: if an earlier deployment created the type with
-- request_id text, drop and recreate (cascade drops dependent functions which
-- downstream files re-create).

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_type WHERE typname = 'mcp_request' AND typnamespace = 'api'::regnamespace)
       AND EXISTS (
           SELECT 1
           FROM pg_attribute a
           JOIN pg_type t ON a.attrelid = t.typrelid
           WHERE t.typname = 'mcp_request'
             AND t.typnamespace = 'api'::regnamespace
             AND a.attname = 'request_id'
             AND a.atttypid = 'text'::regtype
       ) THEN
        DROP TYPE api.mcp_request CASCADE;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'mcp_request' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.mcp_request AS (
            arguments jsonb,
            uri text,
            context jsonb,
            request_id jsonb
        );
    END IF;
END $$;

-- ============================================================================
-- HTTP Response Type (Unified for REST and RPC)
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'http_response' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.http_response AS (
            status_code integer,
            headers extensions.hstore,
            content bytea
        );
    END IF;
END $$;

-- ============================================================================
-- MCP Response Type (JSON-RPC 2.0 Compliant)
-- ============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'mcp_response' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.mcp_response AS (
            envelope jsonb
        );
    END IF;
END $$;

-- ============================================================================
-- Schema Domain Types
-- ============================================================================
-- Reusable domain types describing handler request/response structure.
-- api.handler references these to model the handler contract in JSON or XML.
-- The JSON Schema domain validates top-level keyword shapes at DDL time via
-- api.is_valid_json_schema — this catches malformed schemas before they reach
-- runtime. Not a full JSON Schema validator; does not recurse into subschemas.

CREATE OR REPLACE FUNCTION api.is_valid_json_schema(p_value jsonb)
RETURNS boolean
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH root AS (SELECT p_value AS v)
    SELECT
        -- Root must be object (real schema), boolean (trivial true/false), or NULL
        (v IS NULL OR jsonb_typeof(v) IN ('object', 'boolean'))

        -- Reject empty objects: inputSchema/outputSchema MUST describe shape
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR v <> '{}'::jsonb)

        -- Object-valued schemas must carry at least one recognised keyword
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             v ? 'type' OR v ? '$ref' OR v ? 'enum' OR v ? 'const'
             OR v ? 'allOf' OR v ? 'anyOf' OR v ? 'oneOf' OR v ? 'not'
             OR v ? 'properties' OR v ? 'items' OR v ? 'additionalProperties'
             OR v ? '$id' OR v ? '$schema' OR v ? '$defs' OR v ? 'definitions'
        ))

        -- String-typed keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? '$schema'     OR jsonb_typeof(v->'$schema') = 'string')
             AND (NOT v ? '$id'     OR jsonb_typeof(v->'$id') = 'string')
             AND (NOT v ? '$ref'    OR jsonb_typeof(v->'$ref') = 'string')
             AND (NOT v ? 'title'   OR jsonb_typeof(v->'title') = 'string')
             AND (NOT v ? 'description' OR jsonb_typeof(v->'description') = 'string')
             AND (NOT v ? '$comment'    OR jsonb_typeof(v->'$comment') = 'string')
             AND (NOT v ? 'format'      OR jsonb_typeof(v->'format') = 'string')
             AND (NOT v ? 'pattern'     OR jsonb_typeof(v->'pattern') = 'string')
        ))

        -- Object-valued keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'properties'        OR jsonb_typeof(v->'properties') = 'object')
             AND (NOT v ? 'patternProperties' OR jsonb_typeof(v->'patternProperties') = 'object')
             AND (NOT v ? '$defs'         OR jsonb_typeof(v->'$defs') = 'object')
             AND (NOT v ? 'definitions'   OR jsonb_typeof(v->'definitions') = 'object')
             AND (NOT v ? 'dependentSchemas'  OR jsonb_typeof(v->'dependentSchemas') = 'object')
             AND (NOT v ? 'dependentRequired' OR jsonb_typeof(v->'dependentRequired') = 'object')
             AND (NOT v ? 'dependencies'      OR jsonb_typeof(v->'dependencies') = 'object')
        ))

        -- Array-valued keywords with element shape checks
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'enum'        OR jsonb_typeof(v->'enum') = 'array')
             AND (NOT v ? 'allOf'   OR jsonb_typeof(v->'allOf') = 'array')
             AND (NOT v ? 'anyOf'   OR jsonb_typeof(v->'anyOf') = 'array')
             AND (NOT v ? 'oneOf'   OR jsonb_typeof(v->'oneOf') = 'array')
             AND (NOT v ? 'prefixItems' OR jsonb_typeof(v->'prefixItems') = 'array')
             AND (NOT v ? 'required' OR (
                 jsonb_typeof(v->'required') = 'array'
                 AND NOT EXISTS (
                     SELECT 1 FROM jsonb_array_elements(v->'required') e
                     WHERE jsonb_typeof(e) <> 'string'
                 )
             ))
        ))

        -- type keyword: string OR array of strings from the known type set
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR NOT v ? 'type' OR (
             (jsonb_typeof(v->'type') = 'string'
              AND v->>'type' IN ('string','number','integer','boolean','object','array','null'))
             OR (jsonb_typeof(v->'type') = 'array' AND NOT EXISTS (
                 SELECT 1 FROM jsonb_array_elements(v->'type') e
                 WHERE jsonb_typeof(e) <> 'string'
                    OR e->>0 IS NULL
                    OR (e::text)::text NOT IN
                       ('"string"','"number"','"integer"','"boolean"','"object"','"array"','"null"')
             ))
        ))

        -- Boolean-or-schema keywords
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR (
             (NOT v ? 'not'                       OR jsonb_typeof(v->'not') IN ('object', 'boolean'))
             AND (NOT v ? 'if'                    OR jsonb_typeof(v->'if') IN ('object', 'boolean'))
             AND (NOT v ? 'then'                  OR jsonb_typeof(v->'then') IN ('object', 'boolean'))
             AND (NOT v ? 'else'                  OR jsonb_typeof(v->'else') IN ('object', 'boolean'))
             AND (NOT v ? 'contains'              OR jsonb_typeof(v->'contains') IN ('object', 'boolean'))
             AND (NOT v ? 'propertyNames'         OR jsonb_typeof(v->'propertyNames') IN ('object', 'boolean'))
             AND (NOT v ? 'additionalProperties'  OR jsonb_typeof(v->'additionalProperties') IN ('object', 'boolean'))
             AND (NOT v ? 'unevaluatedProperties' OR jsonb_typeof(v->'unevaluatedProperties') IN ('object', 'boolean'))
             AND (NOT v ? 'additionalItems'       OR jsonb_typeof(v->'additionalItems') IN ('object', 'boolean'))
             AND (NOT v ? 'unevaluatedItems'      OR jsonb_typeof(v->'unevaluatedItems') IN ('object', 'boolean'))
        ))

        -- items: object | array | boolean
        AND (v IS NULL OR jsonb_typeof(v) <> 'object' OR NOT v ? 'items'
             OR jsonb_typeof(v->'items') IN ('object', 'array', 'boolean'))
    FROM root;
$$;

COMMENT ON FUNCTION api.is_valid_json_schema(jsonb) IS
    'Structural JSON Schema validator used by the api.json_schema domain. Rejects empty {}, unknown type names, non-string required[] elements, and malformed keyword shapes. Does not recurse into subschemas.';

-- Redeploy guard: if the domain was created earlier with seven legacy
-- constraints, drop+recreate using the consolidated validator.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint c
        JOIN pg_type t ON c.contypid = t.oid
        WHERE t.typname = 'json_schema'
          AND t.typnamespace = 'api'::regnamespace
          AND c.conname LIKE 'json_schema\_0%' ESCAPE '\'
    ) THEN
        DROP DOMAIN api.json_schema CASCADE;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'json_schema' AND typnamespace = 'api'::regnamespace) THEN
        CREATE DOMAIN api.json_schema AS jsonb
            CONSTRAINT json_schema_valid_shape
                CHECK (api.is_valid_json_schema(VALUE));
    END IF;
END $$;

COMMENT ON DOMAIN api.json_schema IS
    'JSON Schema document describing request or response structure for handlers. Validated by api.is_valid_json_schema at DDL time (single CHECK). Empty {} is rejected — inputSchema/outputSchema MUST describe shape.';

CREATE OR REPLACE FUNCTION api.is_xml_schema_document(p_value xml)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN p_value IS NOT DOCUMENT THEN false
        ELSE xpath_exists(
            '/xsd:schema',
            p_value,
            ARRAY[ARRAY['xsd', 'http://www.w3.org/2001/XMLSchema']]
        )
    END
$$;

COMMENT ON FUNCTION api.is_xml_schema_document(xml) IS
    'Returns true when the value is an XML document whose root element is xsd:schema in the W3C XML Schema namespace. Shallow shape check, not full XSD validation.';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'xml_schema' AND typnamespace = 'api'::regnamespace) THEN
        CREATE DOMAIN api.xml_schema AS xml
            CONSTRAINT xml_schema_must_be_xsd_document
                CHECK (
                    VALUE IS NULL
                    OR api.is_xml_schema_document(VALUE)
                );
    END IF;
END $$;

COMMENT ON DOMAIN api.xml_schema IS
    'XML Schema document describing request or response structure for handlers. '
    'Constraint validates the root element is xsd:schema in the W3C XML Schema namespace.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.handler_type - protocol handler enum';
    RAISE NOTICE '  ✓ api.rest_request - REST request type (method, url, headers, content bytea)';
    RAISE NOTICE '  ✓ api.rpc_request - RPC request type (route_id, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_request - MCP request type (arguments, uri, context, request_id jsonb)';
    RAISE NOTICE '  ✓ api.http_response - unified HTTP response (status_code, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_response - MCP response type (JSON-RPC 2.0 envelope)';
    RAISE NOTICE '  ✓ api.json_schema - JSON Schema domain validated by api.is_valid_json_schema';
    RAISE NOTICE '  ✓ api.xml_schema - XML Schema domain with XSD root-element check';
END $$;
