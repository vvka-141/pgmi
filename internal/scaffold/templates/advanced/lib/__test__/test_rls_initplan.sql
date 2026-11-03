-- ============================================================================
-- Test: every identity reader in a row policy is wrapped as (SELECT ...)
-- ============================================================================
-- A policy calling api.current_organization_id() bare runs it once per
-- candidate row; written (SELECT api.current_organization_id()) PostgreSQL
-- evaluates it once per query as an InitPlan. Every policy of an application
-- schema is deparsed, wrapped calls are removed, and any reader call left
-- fails the deploy, naming the policy.
-- ============================================================================

CREATE FUNCTION pg_temp.unwrapped_reader_policies()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    WITH re AS (
        SELECT '((api)\.)?(current_[a-z_]+|is_authenticated|is_platform_operator|has_permission)' AS fn
    )
    SELECT format('%I.%I %I', p.schemaname, p.tablename, p.policyname)
    FROM pg_policies p
    JOIN pg_temp.application_schema() s ON s.name = p.schemaname
    CROSS JOIN re
    WHERE regexp_replace(concat_ws(' ', p.qual, p.with_check),
                         '\(\s*SELECT\s+' || re.fn || '\((''[^'']*''(::text)?)?\)(\s+AS\s+\w+)?\s*\)',
                         '', 'g')
          ~ ('\m' || re.fn || '\(')
$$;

DO $$
DECLARE
    v_bad text;
BEGIN
    SELECT string_agg(r, E'\n  ' ORDER BY r) INTO v_bad FROM pg_temp.unwrapped_reader_policies() r;
    IF v_bad IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: policies call an identity reader without (SELECT ...):\n  %', v_bad
            USING HINT = 'Write (SELECT api.current_organization_id()), so the reader runs once per query, not once per row.';
    END IF;

    IF NOT EXISTS (SELECT FROM pg_policies WHERE qual ~ '\(\s*SELECT\s+(api\.)?current_organization_id\(\)') THEN
        RAISE EXCEPTION 'TEST FAILED: no policy holds a wrapped current_organization_id(); the deparsed form changed and the guard is blind';
    END IF;

    CREATE TABLE core.initplan_probe (organization_id uuid);
    CREATE POLICY initplan_probe_bare ON core.initplan_probe FOR SELECT
        USING (organization_id = api.current_organization_id() OR api.has_permission('probe.read'));
    CREATE POLICY initplan_probe_wrapped ON core.initplan_probe FOR UPDATE
        USING (organization_id = (SELECT api.current_organization_id()) OR (SELECT api.has_permission('probe.read')));
    SELECT string_agg(r, ', ' ORDER BY r) INTO v_bad FROM pg_temp.unwrapped_reader_policies() r;
    IF v_bad IS DISTINCT FROM 'core.initplan_probe initplan_probe_bare' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name the bare call and pass the wrapped one, named: %', v_bad;
    END IF;
END $$;
