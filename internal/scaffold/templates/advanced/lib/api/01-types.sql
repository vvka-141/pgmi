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

DO $$ BEGIN
    RAISE NOTICE '  ✓ api.handler_type - protocol handler enum';
    RAISE NOTICE '  ✓ api.rest_request - REST request type (method, url, headers, content bytea)';
    RAISE NOTICE '  ✓ api.rpc_request - RPC request type (route_id, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_request - MCP request type (arguments, uri, context, request_id)';
    RAISE NOTICE '  ✓ api.http_response - unified HTTP response (status_code, headers, content bytea)';
    RAISE NOTICE '  ✓ api.mcp_response - MCP response type (JSON-RPC 2.0 envelope)';
END $$;
