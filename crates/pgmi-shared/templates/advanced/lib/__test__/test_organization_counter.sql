-- ============================================================================
-- Test: each organization counts on its own
-- ============================================================================

DO $$
DECLARE
    v_a     uuid := pg_temp.test_login('test', 'counter-a', 'counter-a@example.test');
    v_b     uuid := pg_temp.test_login('test', 'counter-b', 'counter-b@example.test');
    v_org_a uuid;
    v_org_b uuid;
    v_got   bigint[];
BEGIN
    v_org_a := (membership.fn_create_organization_with_owner('Counter A', 'counter-a', v_a)).object_id;
    v_org_b := (membership.fn_create_organization_with_owner('Counter B', 'counter-b', v_b)).object_id;

    PERFORM pg_temp.test_act_as('test|counter-a', v_org_a);
    v_got := ARRAY[core.next_organization_number('task'), core.next_organization_number('task'),
                   core.next_organization_number('document')];
    PERFORM pg_temp.test_act_as('test|counter-b', v_org_b);
    v_got := v_got || core.next_organization_number('task');
    IF v_got IS DISTINCT FROM ARRAY[1, 2, 1, 1]::bigint[] THEN
        RAISE EXCEPTION 'TEST FAILED: counters must run per organization and name, got %', v_got;
    END IF;

    PERFORM pg_temp.test_act_as(NULL);
    BEGIN
        PERFORM core.next_organization_number('task');
        RAISE EXCEPTION 'TEST FAILED: a request without an organization got a number';
    EXCEPTION WHEN insufficient_privilege THEN
        IF SQLERRM NOT LIKE '%the request has no organization%' THEN
            RAISE;
        END IF;
    END;
END $$;
