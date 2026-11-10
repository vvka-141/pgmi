DO $$
DECLARE
    v_open text;
BEGIN
    -- PostgreSQL grants EXECUTE on every new function to PUBLIC, so any role
    -- that can connect could call it.
    SELECT string_agg(p.oid::regprocedure::text, ', ' ORDER BY p.oid::regprocedure::text)
    INTO v_open
    FROM pg_proc p
    WHERE p.pronamespace = 'public'::regnamespace
      AND has_function_privilege('public', p.oid, 'EXECUTE')
      AND NOT EXISTS (
          SELECT 1 FROM pg_depend d
          WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e'
      );

    IF v_open IS NOT NULL THEN
        RAISE EXCEPTION 'Functions executable by PUBLIC: %', v_open;
    END IF;

    CREATE FUNCTION public.privilege_probe() RETURNS int LANGUAGE sql AS 'SELECT 1';
    IF has_function_privilege('public', 'public.privilege_probe()', 'EXECUTE') THEN
        RAISE EXCEPTION 'A function created during the deploy is executable by PUBLIC: default privileges are not closed';
    END IF;
END $$;
