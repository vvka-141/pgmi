/*
<pgmi-meta
    id="a7f01000-0001-4000-8000-000000000001"
    idempotent="false">
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

CREATE TYPE api.handler_type AS ENUM (
    'rest',
    'rpc',
    'mcp_tool',
    'mcp_resource',
    'mcp_prompt'
);

COMMENT ON TYPE api.handler_type IS
    'Protocol discriminator for handler registration. Determines which route table and gateway a handler uses.';

-- ============================================================================
-- REST Request Type
-- ============================================================================

CREATE TYPE api.rest_request AS (
    method text,
    url text,
    headers extensions.hstore,
    content bytea,
    path_params jsonb
);

COMMENT ON TYPE api.rest_request IS
    'Inbound REST request envelope passed to handler functions. Content is raw bytea — use api.content_json() or api.content_text() to decode. path_params holds the route template''s parameters by name, percent-decoded and already checked against their declared types: ((request).path_params->>''id'')::uuid.';

-- ============================================================================
-- RPC Request Type
-- ============================================================================

CREATE TYPE api.rpc_request AS (
    route_id uuid,
    headers extensions.hstore,
    content bytea
);

COMMENT ON TYPE api.rpc_request IS
    'Inbound JSON-RPC request envelope. Route is pre-resolved to a handler UUID; content is the raw request body.';

-- ============================================================================
-- MCP Request Type
-- ============================================================================
-- request_id is jsonb to preserve JSON-RPC 2.0 id semantics (string, integer,
-- or null).

CREATE TYPE api.mcp_request AS (
    arguments jsonb,
    uri text,
    context jsonb,
    request_id jsonb
);

COMMENT ON TYPE api.mcp_request IS
    'Inbound MCP request envelope. request_id is jsonb to preserve JSON-RPC 2.0 id semantics (string, integer, or null).';

-- ============================================================================
-- HTTP Response Type (Unified for REST and RPC)
-- ============================================================================

CREATE TYPE api.http_response AS (
    status_code integer,
    headers extensions.hstore,
    content bytea
);

COMMENT ON TYPE api.http_response IS
    'Outbound HTTP response envelope shared by REST and RPC gateways. Content is raw bytea for protocol neutrality.';

-- ============================================================================
-- MCP Response Type (JSON-RPC 2.0 Compliant)
-- ============================================================================

CREATE TYPE api.mcp_response AS (
    envelope jsonb
);

COMMENT ON TYPE api.mcp_response IS
    'Outbound MCP response. Single jsonb envelope field contains the full JSON-RPC 2.0 result or error object.';

-- ============================================================================
-- Schema Domain Types
-- ============================================================================
-- Reusable domain types describing handler request/response structure.
-- api.handler references these to model the handler contract in JSON or XML.
-- Their CHECKs call the validators in 01-types-validators.sql, which runs
-- before this file.

CREATE DOMAIN api.json_schema AS jsonb
    CONSTRAINT json_schema_valid_shape
        CHECK (api.is_valid_json_schema(VALUE));

COMMENT ON DOMAIN api.json_schema IS
    'JSON Schema document describing request or response structure for handlers. Validated by api.is_valid_json_schema at DDL time (single CHECK). Empty {} is rejected — inputSchema/outputSchema MUST describe shape.';

CREATE DOMAIN api.xml_schema AS xml
    CONSTRAINT xml_schema_must_be_xsd_document
        CHECK (
            VALUE IS NULL
            OR api.is_xml_schema_document(VALUE)
        );

COMMENT ON DOMAIN api.xml_schema IS
    'XML Schema document describing request or response structure for handlers. '
    'Constraint validates the root element is xsd:schema in the W3C XML Schema namespace.';

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.handler_type - protocol handler enum';
    RAISE NOTICE '  ✓ api.rest_request - REST request type (method, url, headers, content bytea, path_params jsonb)';
    RAISE NOTICE '  ✓ api.rpc_request - RPC request type (route_id, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_request - MCP request type (arguments, uri, context, request_id jsonb)';
    RAISE NOTICE '  ✓ api.http_response - unified HTTP response (status_code, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_response - MCP response type (JSON-RPC 2.0 envelope)';
    RAISE NOTICE '  ✓ api.json_schema - JSON Schema domain validated by api.is_valid_json_schema';
    RAISE NOTICE '  ✓ api.xml_schema - XML Schema domain with XSD root-element check';
END $$;
