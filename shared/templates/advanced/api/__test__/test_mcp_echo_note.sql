-- ============================================================================
-- Test: the shipped echo_note tool refuses an argument its schema does not
-- declare (additionalProperties: false)
-- ============================================================================

DO $$
DECLARE
    v_context  jsonb := pg_temp.test_mcp_context(current_setting('test.alice_subject'), current_setting('test.acme_org_id')::uuid);
    v_envelope jsonb;
BEGIN
    v_envelope := (api.mcp_call_tool('echo_note', '{"note": "a"}', v_context, '"e1"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'false'::jsonb
       OR v_envelope->'result'->'structuredContent'->>'note' IS DISTINCT FROM 'a'
       OR (v_envelope->'result'->'structuredContent' ?& ARRAY['asOf', 'hints', 'nextActions']) IS NOT TRUE THEN
        RAISE EXCEPTION 'TEST FAILED: echo_note answers its note with asOf, hints and nextActions, got %', v_envelope;
    END IF;
    v_envelope := (api.mcp_call_tool('echo_note', '{"note": "a", "extra": 1}', v_context, '"e2"')).envelope;
    IF v_envelope->'result'->'isError' IS DISTINCT FROM 'true'::jsonb
       OR coalesce(v_envelope->'result'->'content'->0->>'text', '') NOT LIKE '%extra is not allowed%' THEN
        RAISE EXCEPTION 'TEST FAILED: echo_note must refuse an undeclared argument by name, got %', v_envelope;
    END IF;
END $$;
