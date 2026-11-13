-- Tests for core.apply_org_rls(): the one-call multi-tenant RLS guardrail.
-- Runs in a savepoint, so the throwaway table and its policies roll back.

-- ############################################################################
-- POLICIES - core.apply_org_rls() forces RLS and installs the tenant policies
-- ############################################################################
DO $$
DECLARE
    v_force   boolean;
    v_enabled boolean;
    v_count   int;
    v_qual    text;
BEGIN
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

    -- The select policy keys on the request's organization, not a vacuous TRUE
    SELECT qual INTO v_qual
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe' AND policyname = 'rls_probe_select';
    IF (v_qual !~ 'current_organization_id') IS NOT FALSE THEN
        RAISE EXCEPTION 'apply_org_rls: select policy is not org-scoped (qual: %)', v_qual;
    END IF;

    -- The insert policy pins created_by_user_id when the column is present
    SELECT with_check INTO v_qual
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe' AND policyname = 'rls_probe_insert';
    IF (v_qual !~ 'created_by_user_id') IS NOT FALSE THEN
        RAISE EXCEPTION 'apply_org_rls: insert policy does not pin created_by_user_id (with_check: %)', v_qual;
    END IF;
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
    IF (v_qual !~ 'current_organization_id') IS NOT FALSE THEN
        RAISE EXCEPTION 'apply_org_rls: insert policy not org-scoped without created_by';
    END IF;
END $$;

-- ############################################################################
-- BEHAVIOUR - the policies scope real rows to the organization, read as the owner
-- Everything above reads pg_class and pg_policies:
-- break api.current_organization_id() so it matches every org, or revert FORCE to
-- ENABLE-only, and the catalog is unchanged and every assertion still passes —
-- while every domain table built on this helper leaks across tenants.
--
-- So this block inserts real rows and reads them back as the owner, which runs
-- every handler: the only thing that can tell a scoped policy from a
-- decorative one.
-- ############################################################################
DO $$
DECLARE
    v_alice    uuid;
    v_bob      uuid;
    v_org_a    uuid;
    v_org_b    uuid;
    v_count    int;
    v_affected int;
    v_denied   boolean := false;
BEGIN
    v_alice := pg_temp.test_login('google', 'rls-alice', 'rls-alice@example.test', 'RLS Alice', true);
    v_bob := pg_temp.test_login('github', 'rls-bob', 'rls-bob@example.test', 'RLS Bob', true);

    v_org_a := (membership.fn_create_organization_with_owner('RLS Alice Org', 'rls-alice-org', v_alice)).object_id;
    v_org_b := (membership.fn_create_organization_with_owner('RLS Bob Org', 'rls-bob-org', v_bob)).object_id;

    CREATE TABLE core.rls_behavior_probe (
        id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
        organization_id    uuid NOT NULL,
        created_by_user_id uuid NOT NULL,
        label              text
    );
    PERFORM core.apply_org_rls('core.rls_behavior_probe');

    -- Seeded as the owner. FORCE applies to the owner too, so the identity has
    -- to be Alice's here as well — which is itself part of what is under test.
    PERFORM pg_temp.test_act_as('google|rls-alice', v_org_a);
    INSERT INTO core.rls_behavior_probe (organization_id, created_by_user_id, label)
    VALUES (v_org_a, v_alice, 'alice row');

    PERFORM pg_temp.test_act_as('github|rls-bob', v_org_b);
    INSERT INTO core.rls_behavior_probe (organization_id, created_by_user_id, label)
    VALUES (v_org_b, v_bob, 'bob row');

    -- Alice belongs to both organizations; a request works in one of them.
    PERFORM pg_temp.test_act_as(NULL);
    PERFORM membership.fn_add_member(v_org_b, v_alice, 'org.member');

    -- A SECURITY DEFINER kernel with no predicate of its own. This is the
    -- FORCE claim in lib/core/foundation.sql: the body runs as the table owner,
    -- and only FORCE keeps it tenant-scoped.
    CREATE FUNCTION core.rls_probe_kernel() RETURNS bigint
    LANGUAGE sql SECURITY DEFINER SET search_path = core, api, pg_temp
    AS 'SELECT count(*) FROM core.rls_behavior_probe';

    PERFORM pg_temp.test_act_as('google|rls-alice', v_org_a);

    SELECT count(*) INTO v_count FROM core.rls_behavior_probe;
    IF v_count IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice sees % row(s), want 1 — the select policy is not org-scoped', v_count;
    END IF;

    SELECT count(*) INTO v_count
    FROM core.rls_behavior_probe WHERE organization_id = v_org_b;
    IF v_count IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice read % row(s) of an organization that is not the request''s', v_count;
    END IF;

    -- The FORCE probe, from the same session.
    SELECT core.rls_probe_kernel() INTO v_count;
    IF v_count IS DISTINCT FROM 1 THEN
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
        RAISE EXCEPTION 'apply_org_rls: Alice inserted a row into another organization';
    END IF;

    -- An out-of-scope row is invisible to UPDATE and DELETE, so both are no-ops
    -- rather than errors. Zero rows affected is the assertion.
    UPDATE core.rls_behavior_probe SET label = 'tampered' WHERE organization_id = v_org_b;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    IF v_affected IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice updated % row(s) of another organization', v_affected;
    END IF;

    DELETE FROM core.rls_behavior_probe WHERE organization_id = v_org_b;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    IF v_affected IS DISTINCT FROM 0 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice deleted % row(s) of another organization', v_affected;
    END IF;

    -- No WHERE: an UPDATE that reads no column needs no SELECT rights, so only
    -- the UPDATE policy's WITH CHECK can refuse the new row.
    v_denied := false;
    BEGIN
        UPDATE core.rls_behavior_probe SET organization_id = v_org_b;
    EXCEPTION WHEN insufficient_privilege THEN
        v_denied := true;
    END;
    SELECT count(*) INTO v_count FROM core.rls_behavior_probe WHERE organization_id = v_org_a;
    IF v_denied IS DISTINCT FROM true OR v_count IS DISTINCT FROM 1::bigint THEN
        RAISE EXCEPTION 'apply_org_rls: Alice moved her row into another organization (refused: %, rows left in hers: %)', v_denied, v_count;
    END IF;

    -- Positive control: her own row is writable, so the policies are not simply
    -- denying everything.
    UPDATE core.rls_behavior_probe SET label = 'alice row v2' WHERE organization_id = v_org_a;
    GET DIAGNOSTICS v_affected = ROW_COUNT;
    PERFORM pg_temp.test_act_as(NULL);

    IF v_affected IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'apply_org_rls: Alice could not update her own row (% affected) — the policies deny everything',
            v_affected;
    END IF;
END $$;

