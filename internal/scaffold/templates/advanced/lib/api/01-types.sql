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

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'mcp_request' AND typnamespace = 'api'::regnamespace) THEN
        CREATE TYPE api.mcp_request AS (
            arguments jsonb,
            uri text,
            context jsonb,
            request_id text
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
-- The JSON Schema domain validates top-level keyword shapes (string, object,
-- array, boolean-or-schema) at DDL time. This catches malformed schemas
-- before they reach runtime — NOT full JSON Schema validation.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'json_schema' AND typnamespace = 'api'::regnamespace) THEN
        CREATE DOMAIN api.json_schema AS jsonb

            CONSTRAINT json_schema_01_root_is_schema
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) IN ('object', 'boolean')
                )

            CONSTRAINT json_schema_02_common_string_keywords
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        (NOT VALUE ? '$schema'         OR jsonb_typeof(VALUE->'$schema') = 'string')
                        AND (NOT VALUE ? '$id'         OR jsonb_typeof(VALUE->'$id') = 'string')
                        AND (NOT VALUE ? '$ref'        OR jsonb_typeof(VALUE->'$ref') = 'string')
                        AND (NOT VALUE ? 'title'       OR jsonb_typeof(VALUE->'title') = 'string')
                        AND (NOT VALUE ? 'description' OR jsonb_typeof(VALUE->'description') = 'string')
                        AND (NOT VALUE ? '$comment'    OR jsonb_typeof(VALUE->'$comment') = 'string')
                        AND (NOT VALUE ? 'format'      OR jsonb_typeof(VALUE->'format') = 'string')
                        AND (NOT VALUE ? 'pattern'     OR jsonb_typeof(VALUE->'pattern') = 'string')
                    )
                )

            CONSTRAINT json_schema_03_object_keywords
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        (NOT VALUE ? 'properties'            OR jsonb_typeof(VALUE->'properties') = 'object')
                        AND (NOT VALUE ? 'patternProperties' OR jsonb_typeof(VALUE->'patternProperties') = 'object')
                        AND (NOT VALUE ? '$defs'             OR jsonb_typeof(VALUE->'$defs') = 'object')
                        AND (NOT VALUE ? 'definitions'       OR jsonb_typeof(VALUE->'definitions') = 'object')
                        AND (NOT VALUE ? 'dependentSchemas'  OR jsonb_typeof(VALUE->'dependentSchemas') = 'object')
                        AND (NOT VALUE ? 'dependentRequired' OR jsonb_typeof(VALUE->'dependentRequired') = 'object')
                        AND (NOT VALUE ? 'dependencies'      OR jsonb_typeof(VALUE->'dependencies') = 'object')
                    )
                )

            CONSTRAINT json_schema_04_array_keywords
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        (NOT VALUE ? 'required'        OR jsonb_typeof(VALUE->'required') = 'array')
                        AND (NOT VALUE ? 'enum'        OR jsonb_typeof(VALUE->'enum') = 'array')
                        AND (NOT VALUE ? 'allOf'       OR jsonb_typeof(VALUE->'allOf') = 'array')
                        AND (NOT VALUE ? 'anyOf'       OR jsonb_typeof(VALUE->'anyOf') = 'array')
                        AND (NOT VALUE ? 'oneOf'       OR jsonb_typeof(VALUE->'oneOf') = 'array')
                        AND (NOT VALUE ? 'prefixItems' OR jsonb_typeof(VALUE->'prefixItems') = 'array')
                    )
                )

            CONSTRAINT json_schema_05_type_keyword_shape
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        NOT VALUE ? 'type'
                        OR jsonb_typeof(VALUE->'type') IN ('string', 'array')
                    )
                )

            CONSTRAINT json_schema_06_boolean_or_schema_keywords
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        (NOT VALUE ? 'not'                       OR jsonb_typeof(VALUE->'not') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'if'                    OR jsonb_typeof(VALUE->'if') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'then'                  OR jsonb_typeof(VALUE->'then') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'else'                  OR jsonb_typeof(VALUE->'else') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'contains'              OR jsonb_typeof(VALUE->'contains') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'propertyNames'         OR jsonb_typeof(VALUE->'propertyNames') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'additionalProperties'  OR jsonb_typeof(VALUE->'additionalProperties') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'unevaluatedProperties' OR jsonb_typeof(VALUE->'unevaluatedProperties') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'additionalItems'       OR jsonb_typeof(VALUE->'additionalItems') IN ('object', 'boolean'))
                        AND (NOT VALUE ? 'unevaluatedItems'      OR jsonb_typeof(VALUE->'unevaluatedItems') IN ('object', 'boolean'))
                    )
                )

            CONSTRAINT json_schema_07_items_keyword_shape
                CHECK (
                    VALUE IS NULL
                    OR jsonb_typeof(VALUE) <> 'object'
                    OR (
                        NOT VALUE ? 'items'
                        OR jsonb_typeof(VALUE->'items') IN ('object', 'array', 'boolean')
                    )
                );
    END IF;
END $$;

COMMENT ON DOMAIN api.json_schema IS
    'JSON Schema document describing request or response structure for handlers. '
    'Seven named constraints validate top-level keyword shapes (string/object/array/boolean-or-schema).';

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
    RAISE NOTICE '  ✓ api.mcp_request - MCP request type (arguments, uri, context, request_id)';
    RAISE NOTICE '  ✓ api.http_response - unified HTTP response (status_code, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_response - MCP response type (JSON-RPC 2.0 envelope)';
    RAISE NOTICE '  ✓ api.json_schema - JSON Schema domain with seven structural constraints';
    RAISE NOTICE '  ✓ api.xml_schema - XML Schema domain with XSD root-element check';
END $$;
