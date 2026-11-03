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
BEGIN
    IF ('550e8400-e29b-41d4-a716-446655440000' ?> extensions.uuid_nil()) = extensions.uuid_nil() THEN
        RAISE EXCEPTION 'operator ?>(uuid) failed: valid UUID should parse';
    END IF;
    IF ('invalid' ?> extensions.uuid_nil()) IS DISTINCT FROM extensions.uuid_nil() THEN
        RAISE EXCEPTION 'operator ?>(uuid) failed: invalid input should return default';
    END IF;
    IF ('yes' ?> false) IS DISTINCT FROM true THEN
        RAISE EXCEPTION 'operator ?>(boolean) failed: "yes" should parse as true';
    END IF;
    IF ('no' ?> true) IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'operator ?>(boolean) failed: "no" should parse as false';
    END IF;
    IF ('invalid' ?> false) IS DISTINCT FROM false THEN
        RAISE EXCEPTION 'operator ?>(boolean) failed: invalid input should return default';
    END IF;
    IF ('42' ?> 0) IS DISTINCT FROM 42 THEN
        RAISE EXCEPTION 'operator ?>(integer) failed: "42" should parse';
    END IF;
    IF ('invalid' ?> -1) IS DISTINCT FROM -1 THEN
        RAISE EXCEPTION 'operator ?>(integer) failed: invalid input should return default';
    END IF;
    IF ('1705327800000' ?> 0::bigint) IS DISTINCT FROM 1705327800000 THEN
        RAISE EXCEPTION 'operator ?>(bigint) failed: large value should parse';
    END IF;
    IF ('invalid' ?> -1::bigint) IS DISTINCT FROM -1 THEN
        RAISE EXCEPTION 'operator ?>(bigint) failed: invalid input should return default';
    END IF;
    IF ('19.99' ?> 0::numeric) IS DISTINCT FROM 19.99 THEN
        RAISE EXCEPTION 'operator ?>(numeric) failed: decimal should parse';
    END IF;
    IF ('1.5e10' ?> 0::numeric) IS DISTINCT FROM 15000000000 THEN
        RAISE EXCEPTION 'operator ?>(numeric) failed: scientific notation should parse';
    END IF;
    IF ('invalid' ?> (-1)::numeric) IS DISTINCT FROM -1 THEN
        RAISE EXCEPTION 'operator ?>(numeric) failed: invalid input should return default';
    END IF;
    IF ('2 days 3 hours' ?> interval '0') IS DISTINCT FROM make_interval(days => 2, hours => 3) THEN
        RAISE EXCEPTION 'operator ?>(interval) failed: "2 days 3 hours" should parse';
    END IF;
    IF ('P99999999999Y' ?> interval '0') IS DISTINCT FROM interval '0' THEN
        RAISE EXCEPTION 'operator ?>(interval) failed: oversized input should return default';
    END IF;
    IF ('2025-01-15 14:30:00' ?> timestamp '2000-01-01') IS DISTINCT FROM timestamp '2025-01-15 14:30:00' THEN
        RAISE EXCEPTION 'operator ?>(timestamp) failed: ISO-8601 format should parse';
    END IF;
    IF ('2025-01-15' ?> date '2000-01-01') IS DISTINCT FROM date '2025-01-15' THEN
        RAISE EXCEPTION 'operator ?>(date) failed: ISO date should parse';
    END IF;
    IF ('nope' ?> date '2000-01-01') IS DISTINCT FROM date '2000-01-01' THEN
        RAISE EXCEPTION 'operator ?>(date) failed: invalid input should return default';
    END IF;
    IF ('10.0.0.1' ?> NULL::inet) IS DISTINCT FROM inet '10.0.0.1' THEN
        RAISE EXCEPTION 'operator ?>(inet) failed: an address should parse';
    END IF;
    IF ('invalid' ?> timestamp '2000-01-01') IS DISTINCT FROM timestamp '2000-01-01' THEN
        RAISE EXCEPTION 'operator ?>(timestamp) failed: invalid input should return default';
    END IF;
END $$;
