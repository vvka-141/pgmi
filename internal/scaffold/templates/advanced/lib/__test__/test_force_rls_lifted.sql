-- core.execute_with_force_rls_lifted: a deploy-time statement sees every
-- tenant's rows of a FORCE RLS table, FORCE is back afterwards and after a
-- failed statement, and no application role may call it.

DO $$
DECLARE
    v_seen   text;
    v_force  boolean;
    v_failed boolean := false;
BEGIN
    CREATE TABLE core.force_rls_probe (tenant text NOT NULL, n int NOT NULL);
    INSERT INTO core.force_rls_probe VALUES ('a', 1), ('b', 2);
    CREATE TABLE core.unforced_rls_probe (n int);
    ALTER TABLE core.force_rls_probe ENABLE ROW LEVEL SECURITY;
    ALTER TABLE core.force_rls_probe FORCE ROW LEVEL SECURITY;
    CREATE POLICY force_rls_probe_tenant ON core.force_rls_probe
        USING (tenant = current_setting('test.tenant', true));

    IF (SELECT count(*) FROM core.force_rls_probe) IS DISTINCT FROM 0::bigint THEN
        RAISE EXCEPTION 'TEST FAILED: the owner without a tenant should see no rows of a FORCE RLS table';
    END IF;

    PERFORM core.execute_with_force_rls_lifted(
        ARRAY['core.force_rls_probe', 'core.unforced_rls_probe']::regclass[],
        $sql$SELECT set_config('test.seen', string_agg(tenant, ',' ORDER BY tenant), true) FROM core.force_rls_probe$sql$);
    v_seen := current_setting('test.seen', true);
    IF v_seen IS DISTINCT FROM 'a,b' THEN
        RAISE EXCEPTION 'TEST FAILED: the lifted statement should see every tenant''s rows, saw %', v_seen;
    END IF;

    SELECT relforcerowsecurity INTO v_force FROM pg_class WHERE oid = 'core.force_rls_probe'::regclass;
    IF v_force IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: FORCE ROW LEVEL SECURITY was not restored';
    END IF;
    SELECT relforcerowsecurity INTO v_force FROM pg_class WHERE oid = 'core.unforced_rls_probe'::regclass;
    IF v_force IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'TEST FAILED: a table without FORCE must not gain it';
    END IF;

    BEGIN
        PERFORM core.execute_with_force_rls_lifted(
            ARRAY['core.force_rls_probe']::regclass[], 'SELECT 1/0 FROM core.force_rls_probe');
    EXCEPTION WHEN division_by_zero THEN
        v_failed := true;
    END;
    SELECT relforcerowsecurity INTO v_force FROM pg_class WHERE oid = 'core.force_rls_probe'::regclass;
    IF (v_failed) IS NOT TRUE OR v_force IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'TEST FAILED: after a failed statement FORCE must be back (failed=%, force=%)', v_failed, v_force;
    END IF;

    IF has_function_privilege(pg_temp.deployment_setting('database_api_role'),
                              'core.execute_with_force_rls_lifted(regclass[], text)', 'EXECUTE')
       OR has_function_privilege(pg_temp.deployment_setting('database_kernel_role'),
                                 'core.execute_with_force_rls_lifted(regclass[], text)', 'EXECUTE')
       OR has_function_privilege('public', 'core.execute_with_force_rls_lifted(regclass[], text)', 'EXECUTE') THEN
        RAISE EXCEPTION 'TEST FAILED: an application role may call core.execute_with_force_rls_lifted';
    END IF;
END $$;
