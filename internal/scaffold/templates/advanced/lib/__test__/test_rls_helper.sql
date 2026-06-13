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

    IF NOT v_enabled THEN
        RAISE EXCEPTION 'apply_org_rls: row security not ENABLED on core.rls_probe';
    END IF;
    IF NOT v_force THEN
        RAISE EXCEPTION 'apply_org_rls: row security not FORCED on core.rls_probe (owner would bypass)';
    END IF;

    -- All four policies present
    SELECT count(*) INTO v_count
    FROM pg_policies
    WHERE schemaname = 'core' AND tablename = 'rls_probe'
      AND policyname IN ('rls_probe_select', 'rls_probe_insert', 'rls_probe_update', 'rls_probe_delete');
    IF v_count <> 4 THEN
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

DO $$ BEGIN RAISE NOTICE '+ core.apply_org_rls tests passed'; END $$;
