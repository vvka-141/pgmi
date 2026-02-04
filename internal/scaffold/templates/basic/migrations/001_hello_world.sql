-- ============================================================================
-- Hello World Function
-- ============================================================================
-- This demonstrates pgmi's session variable parameter system.
-- Parameters set by pgmi_init_params() in deploy.sql are accessible via
-- current_setting('pgmi.parameter_name') or pgmi_get_param('parameter_name').
--
-- Deploy with default:    pgmi deploy . -d mydb
-- Deploy with custom:     pgmi deploy . -d mydb --param name=Alice
-- ============================================================================

CREATE OR REPLACE FUNCTION public.hello_world()
RETURNS TEXT
LANGUAGE SQL
STABLE  -- STABLE because it reads session variables
AS $$
    -- Access parameter via session variable
    -- COALESCE provides fallback if parameter not set
    SELECT 'Hello, ' || COALESCE(current_setting('pgmi.name', true), 'World') || '!'::TEXT
$$;

COMMENT ON FUNCTION public.hello_world() IS
    'Demonstrates pgmi session variable parameters - see deploy.sql for configuration';

-- ============================================================================
-- Runtime Parameter Access Demo
-- ============================================================================
-- After pgmi_init_params() runs, parameters are available as session variables.
-- This example shows different ways to access them:

DO $$
BEGIN
    RAISE NOTICE '';
    RAISE NOTICE '=== Parameter Access Examples ===';
    RAISE NOTICE 'Direct access: name = %', current_setting('pgmi.name', true);
    RAISE NOTICE 'With fallback: name = %', COALESCE(current_setting('pgmi.name', true), 'World');
    RAISE NOTICE 'Helper function: name = %', pg_temp.pgmi_get_param('name', 'World');
    RAISE NOTICE 'Function result: %', public.hello_world();
    RAISE NOTICE '';
END $$;
