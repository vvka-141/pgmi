/*
<pgmi-meta
    id="f68314f0-ed40-464c-9b67-8788604f6c1d"
    idempotent="false">
  <description>
    The ?> try-cast operators over common.try_cast. Run-once: an operator has no
    OR REPLACE form, and its implementing function is replaced in place by
    cast.sql, so the operator follows every change to it.
  </description>
  <sortKeys>
    <key>001/002</key>
  </sortKeys>
</pgmi-meta>
*/

-- In common, beside common.try_cast. Every pinned search_path names common
-- (lib/__test__/test_common_reach.sql), so they resolve inside handler bodies.

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = uuid,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, uuid) IS
    'Try-cast operator: parse left as UUID or return right as default. Example: ''text'' ?> extensions.uuid_nil()';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = boolean,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, boolean) IS
    'Try-cast operator: parse left as boolean or return right as default. Example: ''yes'' ?> false';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = integer,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, integer) IS
    'Try-cast operator: parse left as integer or return right as default. Example: ''42'' ?> 0';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = bigint,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, bigint) IS
    'Try-cast operator: parse left as bigint or return right as default. Example: ''1705327800000'' ?> 0';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = numeric,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, numeric) IS
    'Try-cast operator: parse left as numeric or return right as default. Example: ''19.99'' ?> 0';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = interval,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, interval) IS
    'Try-cast operator: parse left as interval or return right as default. Example: ''2 days'' ?> ''0''::interval';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = timestamp,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, timestamp) IS
    'Try-cast operator: parse left as timestamp or return right as default. Example: ''2025-01-15'' ?> CURRENT_TIMESTAMP';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = timestamptz,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, timestamptz) IS
    'Try-cast operator: parse left as timestamptz or return right as default. Example: ''2025-01-15T14:30:00+02:00'' ?> CURRENT_TIMESTAMP';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = date,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, date) IS
    'Try-cast operator: parse left as date or return right as default. Example: ''2025-01-15'' ?> CURRENT_DATE';

CREATE OPERATOR common.?> (
    LEFTARG = text,
    RIGHTARG = inet,
    PROCEDURE = common.try_cast
);

COMMENT ON OPERATOR common.?> (text, inet) IS
    'Try-cast operator: parse left as inet or return right as default. Example: ''10.0.0.1'' ?> NULL::inet';

DO $$
DECLARE
    v_case record;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('uuid', ('550e8400-e29b-41d4-a716-446655440000' ?> extensions.uuid_nil())::text, '550e8400-e29b-41d4-a716-446655440000'),
            ('uuid invalid', ('invalid' ?> extensions.uuid_nil())::text, extensions.uuid_nil()::text),
            ('boolean yes', ('yes' ?> false)::text, 'true'),
            ('boolean no', ('no' ?> true)::text, 'false'),
            ('boolean invalid', ('invalid' ?> false)::text, 'false'),
            ('integer', ('42' ?> 0)::text, '42'),
            ('integer invalid', ('invalid' ?> -1)::text, '-1'),
            ('bigint', ('1705327800000' ?> 0::bigint)::text, '1705327800000'),
            ('bigint invalid', ('invalid' ?> -1::bigint)::text, '-1'),
            ('numeric', ('19.99' ?> 0::numeric)::text, '19.99'),
            ('numeric scientific', ('1.5e10' ?> 0::numeric)::text, '15000000000'),
            ('numeric invalid', ('invalid' ?> (-1)::numeric)::text, '-1'),
            ('interval', ('2 days 3 hours' ?> interval '0')::text, make_interval(days => 2, hours => 3)::text),
            ('interval oversized', ('P99999999999Y' ?> interval '0')::text, interval '0'::text),
            ('timestamp', ('2025-01-15 14:30:00' ?> timestamp '2000-01-01')::text, timestamp '2025-01-15 14:30:00'::text),
            ('timestamp invalid', ('invalid' ?> timestamp '2000-01-01')::text, timestamp '2000-01-01'::text),
            ('date', ('2025-01-15' ?> date '2000-01-01')::text, date '2025-01-15'::text),
            ('date invalid', ('nope' ?> date '2000-01-01')::text, date '2000-01-01'::text),
            ('inet', ('10.0.0.1' ?> NULL::inet)::text, inet '10.0.0.1'::text)
        ) AS c(name, got, want)
    LOOP
        IF v_case.got IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION 'operator ?> failed: % gave %, want %', v_case.name, v_case.got, v_case.want;
        END IF;
    END LOOP;
END $$;
