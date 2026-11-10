-- ============================================================================
-- Test: the entity registry lists registered rows under their own RLS
-- ============================================================================

DO $$
DECLARE
    v_a    uuid := pg_temp.test_login('test', 'entity-a', 'entity-a@example.test');
    v_b    uuid := pg_temp.test_login('test', 'entity-b', 'entity-b@example.test');
    v_org_a uuid;
    v_org_b uuid;
    v_seen text;
BEGIN
    v_org_a := (membership.fn_create_organization_with_owner('Entity A', 'entity-a', v_a)).object_id;
    v_org_b := (membership.fn_create_organization_with_owner('Entity B', 'entity-b', v_b)).object_id;
    CREATE TABLE core.registered_probe (
        object_id       core.entity_id PRIMARY KEY DEFAULT gen_random_uuid(),
        organization_id uuid NOT NULL,
        code            text NOT NULL,
        title           text NOT NULL);
    PERFORM core.apply_org_rls('core.registered_probe', p_has_created_by => false);
    PERFORM core.register_entity('core.registered_probe', 'probe_document', $e$t.code || ': ' || t.title$e$);

    PERFORM pg_temp.test_act_as('test|entity-a', v_org_a);
    INSERT INTO core.registered_probe (organization_id, code, title) VALUES (v_org_a, 'D-1', 'Plan');
    PERFORM pg_temp.test_act_as('test|entity-b', v_org_b);
    INSERT INTO core.registered_probe (organization_id, code, title) VALUES (v_org_b, 'D-2', 'Review');
    PERFORM pg_temp.test_act_as('test|entity-a', v_org_a);
    SELECT string_agg(entity_type || ' ' || title, '; ') INTO v_seen FROM core.vw_entity WHERE entity_type = 'probe_document';
    PERFORM pg_temp.test_act_as(NULL);

    IF v_seen IS DISTINCT FROM 'probe_document D-1: Plan' THEN
        RAISE EXCEPTION 'TEST FAILED: core.vw_entity must list organization A''s registered row only, listed: %', v_seen;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_class WHERE oid = 'core.vw_entity'::regclass AND 'security_invoker=true' = ANY (reloptions)) THEN
        RAISE EXCEPTION 'TEST FAILED: core.vw_entity must run with its reader''s rights';
    END IF;
END $$;
