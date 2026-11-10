-- ============================================================================
-- Test: change tracking records each change once, and stays attached
-- ============================================================================
-- Every table core.apply_change_tracking lists in core.change_tracking keeps
-- its trigger, and no table has the trigger without being listed: a dropped
-- trigger fails the deploy, naming the table. On a sample table the trigger
-- records create, update, archive, restore and delete, and nothing for an
-- update that changes no column.
-- ============================================================================

CREATE FUNCTION pg_temp.relation_name(p_relation oid)
RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT format('%s.%I', c.relnamespace::regnamespace, c.relname) FROM pg_class c WHERE c.oid = p_relation),
                    p_relation::text)
$$;

CREATE FUNCTION pg_temp.change_tracking_gap()
RETURNS SETOF text
LANGUAGE sql STABLE AS $$
    SELECT pg_temp.relation_name(t.relation) || ': listed in core.change_tracking without its trigger'
    FROM core.change_tracking t
    WHERE NOT EXISTS (SELECT FROM pg_trigger g
                      WHERE g.tgrelid = t.relation AND g.tgfoid = 'core.capture_change()'::regprocedure
                        AND g.tgenabled IS DISTINCT FROM 'D')
    UNION ALL
    SELECT pg_temp.relation_name(g.tgrelid) || ': has the change trigger without being listed'
    FROM pg_trigger g
    WHERE g.tgfoid = 'core.capture_change()'::regprocedure AND g.tgparentid = 0
      AND NOT EXISTS (SELECT FROM core.change_tracking t WHERE t.relation = g.tgrelid)
$$;

DO $$
DECLARE
    v_gap text;
BEGIN
    SELECT string_agg(g, E'\n  ' ORDER BY g) INTO v_gap FROM pg_temp.change_tracking_gap() g;
    IF v_gap IS NOT NULL THEN
        RAISE EXCEPTION E'TEST FAILED: change tracking is not attached as listed:\n  %', v_gap
            USING HINT = 'Call core.apply_change_tracking(''<table>'') again in a new run-once file.';
    END IF;
END $$;

DO $$
DECLARE
    v_owner uuid := pg_temp.test_login('test', 'tracking-owner', 'tracking-owner@example.test');
    v_org   uuid;
    v_id    uuid := '00000000-0451-4000-8000-000000000001';
    v_log   text;
    v_gap   text;
BEGIN
    v_org := (membership.fn_create_organization_with_owner('Tracking', 'tracking', v_owner)).object_id;
    CREATE TABLE core.tracked_probe (
        object_id       core.entity_id PRIMARY KEY,
        organization_id uuid NOT NULL,
        title           text NOT NULL,
        created_at      timestamptz NOT NULL DEFAULT now(),
        deleted_at      timestamptz);
    PERFORM core.apply_org_rls('core.tracked_probe', p_has_created_by => false);
    PERFORM core.apply_change_tracking('core.tracked_probe');
    PERFORM pg_temp.test_act_as('test|tracking-owner', v_org);

    INSERT INTO core.tracked_probe (object_id, organization_id, title) VALUES (v_id, v_org, 'draft');
    UPDATE core.tracked_probe SET title = 'final' WHERE object_id = v_id;
    UPDATE core.tracked_probe SET title = 'final' WHERE object_id = v_id;
    UPDATE core.tracked_probe SET deleted_at = now() WHERE object_id = v_id;
    UPDATE core.tracked_probe SET deleted_at = NULL WHERE object_id = v_id;
    DELETE FROM core.tracked_probe WHERE object_id = v_id;

    SELECT string_agg(e.action || ' ' || e.changes::text, '; ' ORDER BY e.id) INTO v_log
    FROM core.change_event e
    WHERE e.relation = 'core.tracked_probe' AND e.object_id = v_id
      AND e.principal_id IS NOT DISTINCT FROM v_owner;
    PERFORM pg_temp.test_act_as(NULL);

    IF v_log IS DISTINCT FROM format('create {"title": "draft", "object_id": "%1$s", "organization_id": "%2$s"}; '
                                     'update {"title": "final"}; archive {}; restore {}; '
                                     'delete {"title": "final", "object_id": "%1$s", "organization_id": "%2$s"}', v_id, v_org) THEN
        RAISE EXCEPTION 'TEST FAILED: the change log of the sample row is wrong: %', v_log;
    END IF;

    DROP TRIGGER trg_tracked_probe_change_tracking ON core.tracked_probe;
    SELECT string_agg(g, '; ') INTO v_gap FROM pg_temp.change_tracking_gap() g;
    IF v_gap IS DISTINCT FROM 'core.tracked_probe: listed in core.change_tracking without its trigger' THEN
        RAISE EXCEPTION 'TEST FAILED: the guard must name a tracked table whose trigger was dropped, named: %', v_gap;
    END IF;
END $$;

-- A change records who really made it and as what: the person's own change
-- is human; a change through an approved connection is its agent's, made for
-- the person.
DO $$
DECLARE
    v_owner   uuid := pg_temp.test_login('test', 'tracking-agent', 'tracking-agent@example.test');
    v_org     uuid;
    v_context jsonb;
    v_agent   uuid;
    v_log     text;
BEGIN
    v_org := (membership.fn_create_organization_with_owner('Tracking agent', 'tracking-agent', v_owner)).object_id;
    CREATE TABLE core.tracked_actor_probe (
        object_id       core.entity_id PRIMARY KEY,
        organization_id uuid NOT NULL,
        title           text NOT NULL);
    PERFORM core.apply_org_rls('core.tracked_actor_probe', p_has_created_by => false);
    PERFORM core.apply_change_tracking('core.tracked_actor_probe');
    PERFORM api.create_or_replace_mcp_handler(jsonb_build_object(
        'id', 'ffffffff-0451-4000-8000-000000000002', 'type', 'tool', 'effect', 'write', 'reversible', true,
        'name', 'tracked_actor_probe_write', 'description', 'Writes a tracked row', 'access', 'authenticated'),
        $b$BEGIN
            INSERT INTO core.tracked_actor_probe (object_id, organization_id, title)
            VALUES ('00000000-0451-4000-8000-000000000003', api.current_organization_id(), 'by the agent');
            RETURN api.mcp_tool_result(jsonb_build_array(api.mcp_text('done')), (request).request_id,
                structured_content => jsonb_build_object('agent', api.current_actual_principal_id()));
        END;$b$);

    PERFORM pg_temp.test_act_as('test|tracking-agent', v_org);
    INSERT INTO core.tracked_actor_probe (object_id, organization_id, title)
    VALUES ('00000000-0451-4000-8000-000000000002', v_org, 'by the person');
    v_context := pg_temp.test_mcp_context('test|tracking-agent', v_org);
    v_agent := ((api.mcp_call_tool('tracked_actor_probe_write', '{}'::jsonb, v_context, '"t"'::jsonb)).envelope
                ->'result'->'structuredContent'->>'agent')::uuid;

    PERFORM pg_temp.test_act_as('test|tracking-agent', v_org);
    SELECT string_agg(concat_ws(' ', e.changes->>'title', e.actor_kind,
                                CASE e.principal_id WHEN v_owner THEN 'person' END,
                                CASE e.actual_principal_id WHEN v_owner THEN 'person' WHEN v_agent THEN 'agent' END),
                      '; ' ORDER BY e.id) INTO v_log
    FROM core.change_event e
    WHERE e.relation = 'core.tracked_actor_probe';
    PERFORM pg_temp.test_act_as(NULL);

    IF v_agent IS NULL OR v_agent IS NOT DISTINCT FROM v_owner
       OR v_log IS DISTINCT FROM 'by the person human person person; by the agent agent person agent' THEN
        RAISE EXCEPTION 'TEST FAILED: changes must record the actual principal and actor kind, got agent % and %', v_agent, v_log;
    END IF;
END $$;
