-- Tests for core.apply_org_rls(): the one-call multi-tenant RLS guardrail.
-- Runs in a savepoint, so the throwaway table and its policies roll back.

DO $$
DECLARE
    v_force   boolean;
    v_enabled boolean;
    v_count   int;
    v_qual    text;
BEGIN
    RAISE NOTICE '-> Testing core.apply_org_rls()';

    CREATE TABLE core.rls_probe (
        id                  uuid PRIMARY KEY,
        organization_id     uuid NOT NULL,
        created_by_user_id  uuid NOT NULL,
        label               text
    );

    PERFORM core.apply_org_rls('core.rls_probe');

    -- ENABLE + FORCE both on
    SELECT relrowsecurity, relforcerowsecurity
    INTO v_enabled, v_force
    FROM pg_class WHERE oid = 'core.rls_probe'::regclass;

    IF v_enabled IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'apply_org_rls: row security not ENABLED on core.rls_probe';
    END IF;
    IF v_force IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'apply_org_rls: row security not FORCED on core.rls_probe (owner would bypass)';
    END IF;

    -- All four policies present
    SELECT count(*) INTO v_count
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe'
      AND policyname IN ('rls_probe_select', 'rls_probe_insert', 'rls_probe_update', 'rls_probe_delete');
    IF v_count IS DISTINCT FROM 4 THEN
        RAISE EXCEPTION 'apply_org_rls: expected 4 policies, found %', v_count;
    END IF;

    -- The select policy keys on the org membership anchor, not a vacuous TRUE
    SELECT qual INTO v_qual
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe' AND policyname = 'rls_probe_select';
    IF v_qual !~ 'current_member_org_ids' THEN
        RAISE EXCEPTION 'apply_org_rls: select policy is not org-scoped (qual: %)', v_qual;
    END IF;

    -- The insert policy pins created_by_user_id when the column is present
    SELECT with_check INTO v_qual
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe' AND policyname = 'rls_probe_insert';
    IF v_qual !~ 'created_by_user_id' THEN
        RAISE EXCEPTION 'apply_org_rls: insert policy does not pin created_by_user_id (with_check: %)', v_qual;
    END IF;

    RAISE NOTICE '  + ENABLE+FORCE RLS and four org-scoped policies installed';
END $$;

-- p_has_created_by => false: a table without created_by_user_id is still scoped
DO $$
DECLARE
    v_qual text;
BEGIN
    CREATE TABLE core.rls_probe_no_creator (
        id              uuid PRIMARY KEY,
        organization_id uuid NOT NULL,
        label           text
    );

    PERFORM core.apply_org_rls('core.rls_probe_no_creator', p_has_created_by => false);

    SELECT with_check INTO v_qual
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe_no_creator' AND policyname = 'rls_probe_no_creator_insert';
    IF v_qual ~ 'created_by_user_id' THEN
        RAISE EXCEPTION 'apply_org_rls: insert policy referenced created_by_user_id when p_has_created_by=false';
    END IF;
    IF v_qual !~ 'current_member_org_ids' THEN
        RAISE EXCEPTION 'apply_org_rls: insert policy not org-scoped without created_by';
    END IF;

    RAISE NOTICE '  + p_has_created_by => false omits the created_by predicate';
END $$;

-- ============================================================================
-- Behaviour, not policy text. Everything above reads pg_class and pg_policies:
-- break api.current_member_org_ids() so it returns every org, or revert FORCE to
-- ENABLE-only, and the catalog is unchanged and every assertion still passes —
-- while every domain table built on this helper leaks across tenants.
--
-- So this block inserts real rows and reads them back through a role, which is
-- the only thing that can tell a scoped policy from a decorative one.
-- ============================================================================

DO $$
DECLARE
    v_api_role text := pg_temp.deployment_setting('database_api_role');
    v_alice    uuid;
    v_bob      uuid;
    v_org_a    uuid;
    v_org_b    uuid;
    v_count    int;
    v_affected int;
    v_denied   boolean := false;
BEGIN
    RAISE NOTICE '-> Testing core.apply_org_rls() behaviour through a role';

    v_alice := membership.upsert_user('google', 'rls-alice', 'rls-alice@example.com', 'RLS Alice', true);
    v_bob := membership.upsert_user('github', 'rls-bob', 'rls-bob@example.com', 'RLS Bob', true);

    SELECT object_id INTO STRICT v_org_a
    FROM membership.organization WHERE owner_user_id = v_alice AND is_personal;
    SELECT object_id INTO STRICT v_org_b
    FROM membership.organization WHERE owner_user_id = v_bob AND is_personal;

    CREATE TABLE core.rls_behavior_probe (
        id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
        organization_id    uuid NOT NULL,
        created_by_user_id uuid NOT NULL,
        label              text
    );
    PERFORM core.apply_org_rls('core.rls_behavior_probe');

    -- Seeded as the owner. FORCE applies to the owner too, so the identity has
    -- to be Alice's here as well — which is itself part of what is under test.
    PERFORM set_config('auth.idp_subject', 'google|rls-alice', true);
    INSERT INTO core.rls_behavior_probe (organization_id, created_by_user_id, label)
    VALUES (v_org_a, v_alice, 'alice row');

    PERFORM set_config('auth.idp_subject', 'github|rls-bob', true);
    INSERT INTO core.rls_behavior_probe (organization_id, created_by_user_id, label)
    VALUES (v_org_b, v_bob, 'bob row');

    -- A SECURITY DEFINER kernel with no predicate of its own. This is the
    -- FORCE claim in lib/core/foundation.sql: the body runs as the table owner,
    -- and only FORCE keeps it tenant-scoped.
    CREATE FUNCTION core.rls_probe_kernel() RETURNS bigint
    LANGUAGE sql SECURITY DEFINER SET search_path = core, api, pg_temp
    AS 'SELECT count(*) FROM core.rls_behavior_probe';
    EXECUTE format('GRANT EXECUTE ON FUNCTION core.rls_probe_kernel() TO %I', v_api_role);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON core.rls_behavior_probe TO %I', v_api_role);

    -- Act as Alice through a role that does not own the table.
    PERFORM set_config('auth.idp_subject', 'google|rls-alice', true);
    EXECUTE format('SET ROLE %I', v_api_role);

    SELECT count(*) INTO v_count FROM core.rls_behavior_probe;
    IF v_count IS DISTINCT FROM 1 THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: Alice sees % row(s), want 1 — the select policy is not org-scoped', v_count;
    END IF;

    SELECT count(*) INTO v_count
    FROM core.rls_behavior_probe WHERE organization_id = v_org_b;
    IF v_count IS DISTINCT FROM 0 THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: Alice read % row(s) of another organization', v_count;
    END IF;

    -- The FORCE probe, from the same session.
    SELECT core.rls_probe_kernel() INTO v_count;
    IF v_count IS DISTINCT FROM 1 THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: a SECURITY DEFINER kernel saw % row(s), want 1 — FORCE ROW LEVEL SECURITY is not in effect, so the owner bypasses the policy',
            v_count;
    END IF;

    -- Writing into another organization must be refused, not silently accepted.
    BEGIN
        INSERT INTO core.rls_behavior_probe (organization_id, created_by_user_id, label)
        VALUES (v_org_b, v_alice, 'cross-tenant insert');
    EXCEPTION WHEN insufficient_privilege THEN
        v_denied := true;
    END;
    IF v_denied IS DISTINCT FROM true THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: Alice inserted a row into another organization';
    END IF;

    -- An out-of-scope row is invisible to UPDATE and DELETE, so both are no-ops
    -- rather than errors. Zero rows affected is the assertion.
    UPDATE core.rls_behavior_probe SET label = 'tampered' WHERE organization_id = v_org_b;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    IF v_affected IS DISTINCT FROM 0 THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: Alice updated % row(s) of another organization', v_affected;
    END IF;

    DELETE FROM core.rls_behavior_probe WHERE organization_id = v_org_b;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    IF v_affected IS DISTINCT FROM 0 THEN
        RESET ROLE;
        RAISE EXCEPTION 'apply_org_rls: Alice deleted % row(s) of another organization', v_affected;
    END IF;

    -- Positive control: her own row is writable, so the policies are not simply
    -- denying everything.
    UPDATE core.rls_behavior_probe SET label = 'alice row v2' WHERE organization_id = v_org_a;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    RESET ROLE;

    IF v_affected IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice could not update her own row (% affected) — the policies deny everything',
            v_affected;
    END IF;

    RAISE NOTICE '  + rows are org-scoped for select, insert, update and delete, and FORCE holds through a SECURITY DEFINER kernel';
END $$;

DO $$ BEGIN RAISE NOTICE '+ core.apply_org_rls tests passed'; END $$;
