-- ============================================================================
-- Test: membership tables are closed to every role but the owner and the kernel
-- ============================================================================
-- Requests read membership through the owner's policies and write it only
-- through the kernels. Every membership table has row-level security, a
-- policy for the kernel role, and no grant to any other role but a managed
-- provider's own (provider_role_pattern) SELECT, INSERT, UPDATE or DELETE:
-- the forced policies bind those unless the role bypasses row-level security,
-- as a provider's admin group may. TRUNCATE, TRIGGER, REFERENCES and MAINTAIN
-- act on the whole table; the deploy revokes them and they still fail here;
-- every view is security_invoker, so none reads around that.

CREATE FUNCTION pg_temp.membership_grant_beyond()
RETURNS text LANGUAGE sql STABLE AS $$
    SELECT string_agg(DISTINCT c.relname || ' to ' || CASE a.grantee WHEN 0 THEN 'PUBLIC' ELSE a.grantee::regrole::text END, ', ')
    FROM pg_class c
    CROSS JOIN LATERAL aclexplode(c.relacl) a
    WHERE c.relnamespace = 'membership'::regnamespace AND c.relkind = 'r'
      AND a.grantee IS DISTINCT FROM c.relowner
      AND a.grantee IS DISTINCT FROM pg_temp.deployment_setting('database_kernel_role')::regrole
      AND NOT (pg_temp.provider_role(a.grantee) AND a.privilege_type IN ('SELECT', 'INSERT', 'UPDATE', 'DELETE'))
      AND CASE WHEN a.grantee = 0 THEN true ELSE NOT pg_has_role(a.grantee, c.relowner, 'MEMBER') END
$$;

DO $$
DECLARE
    v_kernel   text := pg_temp.deployment_setting('database_kernel_role');
    v_offender text;
BEGIN
    SELECT string_agg(c.relname, ', ' ORDER BY c.relname) INTO v_offender
    FROM pg_class c
    WHERE c.relnamespace = 'membership'::regnamespace AND c.relkind = 'r'
      AND (NOT c.relrowsecurity
           OR NOT EXISTS (SELECT FROM pg_policy p WHERE p.polrelid = c.oid AND v_kernel::regrole = ANY (p.polroles)));
    IF v_offender IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: membership table(s) without row-level security or a kernel policy: %', v_offender;
    END IF;

    v_offender := pg_temp.membership_grant_beyond();
    IF v_offender IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: membership table(s) granted beyond the owner and the kernel: %', v_offender;
    END IF;

    SELECT string_agg(n.nspname || '.' || c.relname, ', ') INTO v_offender
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind = 'v'
      AND (n.nspname = 'membership' OR (n.nspname = 'api' AND c.relname = 'vw_current_user'))
      AND NOT coalesce('security_invoker=true' = ANY (c.reloptions), false);
    IF v_offender IS NOT NULL THEN
        RAISE EXCEPTION 'TEST FAILED: view(s) over membership without security_invoker: %', v_offender;
    END IF;

    RAISE DEBUG '  + membership tables closed, views security_invoker';
END $$;

-- With provider_role_pattern set, the provider's SELECT drops out; its
-- TRUNCATE and a grant to PUBLIC still fail.
DO $$
DECLARE
    v_owner    text := current_user;
    v_offender text;
BEGIN
    RESET ROLE;
    CREATE ROLE pgmi_provider_probe NOLOGIN;
    EXECUTE format('SET ROLE %I', v_owner);
    GRANT SELECT ON membership.organization TO pgmi_provider_probe;
    GRANT SELECT ON membership.organization TO PUBLIC;
    GRANT TRUNCATE ON membership.connection TO pgmi_provider_probe;
    PERFORM set_config('deployment.provider_role_pattern', 'pgmi\_provider\_%', true);

    v_offender := pg_temp.membership_grant_beyond();
    IF coalesce(v_offender, '') LIKE '%organization to pgmi\_provider\_probe%'
       OR coalesce(v_offender, '') NOT LIKE '%connection to pgmi\_provider\_probe%'
       OR coalesce(v_offender, '') NOT LIKE '%organization to PUBLIC%' THEN
        RAISE EXCEPTION 'TEST FAILED: with provider_role_pattern the provider''s SELECT must drop out and its TRUNCATE and the PUBLIC grant fail, named: %', v_offender;
    END IF;
END $$;
