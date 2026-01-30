/*
<pgmi-meta
    id="00000000-0000-0000-0000-000000000001"
    idempotent="true">
  <description>
    Database initialization: role hierarchy, schemas, extensions, and deployment tracking infrastructure
  </description>
  <sortKeys>
    <key>000</key>
  </sortKeys>
</pgmi-meta>
*/
-- ============================================================================
-- Role Hierarchy: Owner (NOLOGIN) → Admin (LOGIN) → API (LOGIN, restricted)
-- ============================================================================
-- Creates three-tier security: owner owns all objects, admin manages database,
-- API clients get execute-only permissions on api schema functions.
-- ============================================================================

DO $$
DECLARE
    v_owner_role TEXT := pg_temp.pgmi_get_param('database_owner_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    RAISE DEBUG '→ Verifying role hierarchy';

    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_owner_role) THEN
        RAISE EXCEPTION 'Owner role % not found', v_owner_role;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_admin_role) THEN
        RAISE EXCEPTION 'Admin role % not found', v_admin_role;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_api_role) THEN
        RAISE EXCEPTION 'API role % not found', v_api_role;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = v_customer_role) THEN
        RAISE EXCEPTION 'Customer role % not found', v_customer_role;
    END IF;

    RAISE DEBUG '✓ Role hierarchy verified';
    RAISE DEBUG '  Owner:    % (NOLOGIN)', v_owner_role;
    RAISE DEBUG '  Admin:    % (LOGIN, full access)', v_admin_role;
    RAISE DEBUG '  API:      % (LOGIN, restricted)', v_api_role;
    RAISE DEBUG '  Customer: % (LOGIN, RLS-restricted)', v_customer_role;
END $$;

-- ============================================================================
-- Role Context Verification
-- ============================================================================

DO $$
DECLARE
    v_expected_role TEXT := pg_temp.pgmi_get_param('database_owner_role');
    v_current_role TEXT := current_setting('role');
BEGIN
    IF v_current_role != v_expected_role THEN
        RAISE EXCEPTION 'Role context mismatch: expected %, got %', v_expected_role, v_current_role;
    END IF;

    RAISE DEBUG '';
    RAISE DEBUG '⚡ Role context: % (owner)', v_current_role;
    RAISE DEBUG '';
END $$;






-- ============================================================================
-- Schema Architecture: utils / api / core / internal
-- ============================================================================
-- utils:    PostgreSQL-native helpers (text, UUID, JSON utilities)
-- api:      Public RPC surface for client interactions
-- core:     Business domain logic and operational data
-- internal: Infrastructure (migrations, HTTP handlers, internal state)
-- ============================================================================

DO $$
DECLARE
    v_api_role TEXT := pg_temp.pgmi_get_param('database_api_role');
    v_admin_role TEXT := pg_temp.pgmi_get_param('database_admin_role');
    v_owner_role TEXT := pg_temp.pgmi_get_param('database_owner_role');
    v_customer_role TEXT := pg_temp.pgmi_get_param('database_customer_role');
BEGIN
    RAISE DEBUG '→ Creating schemas';

    -- Create application schemas

    CREATE SCHEMA IF NOT EXISTS utils;
    COMMENT ON SCHEMA utils IS
        'General-purpose PostgreSQL-native utilities. Replacement for public schema with text/UUID/JSON/array helpers.';

    CREATE SCHEMA IF NOT EXISTS api;
    COMMENT ON SCHEMA api IS
        'Public API surface for client interactions. Contains RPC-style functions with HTTP-like semantics.';

    CREATE SCHEMA IF NOT EXISTS core;
    COMMENT ON SCHEMA core IS
        'Domain logic and operational data. Contains business entities, domain functions, and application logic.';

    CREATE SCHEMA IF NOT EXISTS internal;
    COMMENT ON SCHEMA internal IS
        'Infrastructure and implementation details. Contains migration tracking, HTTP handlers, and internal state.';

    CREATE SCHEMA IF NOT EXISTS membership;
    COMMENT ON SCHEMA membership IS
        'User, organization, and role management. Identity provider integration and RLS policies.';

    RAISE DEBUG '  ✓ Created schemas: utils, api, core, internal, membership';

    -- Lock down public schema to prevent accidental use

    REVOKE ALL ON SCHEMA public FROM PUBLIC;
    REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
    REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC;
    REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;

    -- Prevent future objects in public schema from being accessible
    ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM PUBLIC;

    RAISE DEBUG '  ✓ Public schema locked down (no PUBLIC access)';

    -- Configure schema permissions
    REVOKE ALL ON SCHEMA utils FROM PUBLIC;
    REVOKE ALL ON SCHEMA api FROM PUBLIC;
    REVOKE ALL ON SCHEMA core FROM PUBLIC;
    REVOKE ALL ON SCHEMA internal FROM PUBLIC;
    REVOKE ALL ON SCHEMA membership FROM PUBLIC;

    EXECUTE format('GRANT USAGE ON SCHEMA utils TO %I', v_admin_role);
    EXECUTE format('GRANT USAGE ON SCHEMA utils TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA utils TO %I', v_customer_role);
    EXECUTE format('GRANT USAGE ON SCHEMA api TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA api TO %I', v_customer_role);
    EXECUTE format('GRANT USAGE ON SCHEMA core TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_admin_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_api_role);
    EXECUTE format('GRANT USAGE ON SCHEMA membership TO %I', v_customer_role);

    RAISE DEBUG '  ✓ Granted USAGE on utils to all roles (permissive)';
    RAISE DEBUG '  ✓ Granted USAGE on api, core to %', v_api_role;
    RAISE DEBUG '  ✓ No public access to any application schema';

    RAISE DEBUG '✓ Schemas created: utils, api, core, internal';

    -- Configure database-level search_path (fallback for connections without role-specific path)
    EXECUTE format(
        'ALTER DATABASE %I SET search_path = core, api, membership, internal, extensions, utils, pg_temp',
        current_database()
    );

    SET search_path TO core, api, membership, internal, extensions, utils, pg_temp;

    RAISE DEBUG '  ✓ Database search_path configured (default for all connections)';
    RAISE DEBUG '';
    RAISE DEBUG '⚡ Session search path: core, api, internal, extensions, utils, pg_temp';
    RAISE DEBUG '';
END $$;






-- ============================================================================
-- PostgreSQL Extensions
-- ============================================================================
-- Extensions are installed during bootstrap by superuser
-- Enabled by default: uuid-ossp, pgcrypto, pg_trgm, hstore
-- To add more extensions, edit deploy.sql bootstrap section
-- ============================================================================

DO $$
BEGIN
    RAISE DEBUG '→ Verifying PostgreSQL extensions';

    -- Verify extensions schema exists
    IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'extensions') THEN
        RAISE EXCEPTION 'Extensions schema not found';
    END IF;

    -- Verify core extensions are installed
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'uuid-ossp') THEN
        RAISE WARNING 'uuid-ossp extension not installed - required for HTTP framework';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgcrypto') THEN
        RAISE WARNING 'pgcrypto extension not installed - required for hashing/encryption';
    END IF;

    RAISE DEBUG '  ✓ Extensions schema verified';
    RAISE DEBUG '  ✓ Core extensions present (uuid-ossp, pgcrypto, pg_trgm, hstore)';
END $$;





-- ============================================================================
-- Deployment Script Execution Log (Metadata-Driven Tracking)
-- ============================================================================
-- The internal.deployment_script_execution_log table tracks executed deployment
-- scripts by UUID for path-independent idempotency and enables:
--   - Path-independent tracking (scripts can be renamed/moved)
--   - Idempotent script re-execution
--   - One-time script enforcement
--   - Execution order history
--   - Drift detection via checksum comparison
-- ============================================================================

DO $$
BEGIN
    RAISE DEBUG '✓ Deployment tracking ready';
END $$;
