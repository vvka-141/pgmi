/*
<pgmi-meta
    id="152e0f54-11dd-437c-981c-fadd27353c63"
    idempotent="true">
  <description>
    core.next_organization_number(counter): the request's organization's next
    number for the counter, 1 first.
  </description>
  <sortKeys>
    <key>005/000/121</key>
  </sortKeys>
</pgmi-meta>
*/

-- One upsert: concurrent callers in one organization queue on the counter's
-- row and get distinct numbers. A rolled-back transaction leaves a gap.
CREATE OR REPLACE FUNCTION core.next_organization_number(p_counter text)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    v_organization uuid := api.current_organization_id();
    v_value        bigint;
BEGIN
    IF v_organization IS NULL THEN
        RAISE EXCEPTION 'next_organization_number(%): the request has no organization', p_counter
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    INSERT INTO core.organization_counter AS c (organization_id, counter, last_value)
    VALUES (v_organization, p_counter, 1)
    ON CONFLICT (organization_id, counter) DO UPDATE SET last_value = c.last_value + 1
    RETURNING c.last_value INTO v_value;
    RETURN v_value;
END $$;

COMMENT ON FUNCTION core.next_organization_number(text) IS
    'The request''s organization''s next number for p_counter (1, 2, ...), e.g. a task number. Gaps on rollback.';
