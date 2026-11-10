/*
<pgmi-meta
    id="39c75443-ec03-4212-9140-755626f56c01"
    idempotent="true">
  <description>
    Type conversion utility functions with fallback defaults (UUID, boolean, integer, bigint, numeric, interval, timestamp, timestamptz, date, inet). Surrounding whitespace is ignored; NaN, infinity and out-of-range values give the default.
  </description>
  <sortKeys>
    <key>001/000</key>
  </sortKeys>
</pgmi-meta>
*/

-- ASCII whitespace of every kind; btrim alone strips only spaces, so a value
-- pasted with a tab or a trailing newline would fall to the default.
CREATE OR REPLACE FUNCTION common.trim_space(input text)
RETURNS text
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE
AS $$
    SELECT btrim($1, E' \t\n\r\f\x0b');
$$;

CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value uuid)
RETURNS uuid
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN common.trim_space($1) ~* '^[0-9a-f]{8}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{4}-?[0-9a-f]{12}$'
        THEN common.trim_space($1)::uuid
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION common.try_cast(text, uuid) IS
    'Try to parse text as UUID, returning default value on failure. Accepts standard (with dashes) and compact (without dashes) formats.';

CREATE OR REPLACE FUNCTION common.uuid_is_nil(input uuid)
RETURNS boolean
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT $1 IS NOT NULL AND $1 = extensions.uuid_nil();
$$;

COMMENT ON FUNCTION common.uuid_is_nil(uuid) IS
    'Check if UUID is nil (all zeros). Uses extensions.uuid_nil() from uuid-ossp extension.';

CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value boolean)
RETURNS boolean
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        ELSE CASE lower(common.trim_space($1))
            WHEN 'true'  THEN true
            WHEN 't'     THEN true
            WHEN 'yes'   THEN true
            WHEN 'y'     THEN true
            WHEN 'on'    THEN true
            WHEN '1'     THEN true
            WHEN 'false' THEN false
            WHEN 'f'     THEN false
            WHEN 'no'    THEN false
            WHEN 'n'     THEN false
            WHEN 'off'   THEN false
            WHEN '0'     THEN false
            ELSE $2
        END
    END;
$$;

COMMENT ON FUNCTION common.try_cast(text, boolean) IS
    'Try to parse text as boolean. Accepts: true/t/yes/y/on/1 (true), false/f/no/n/off/0 (false). Case insensitive. Returns default on unrecognized input.';

-- 32-bit; signs, leading zeros and surrounding whitespace allowed. The length
-- guard keeps a huge digit string from overflowing the numeric range check.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value integer)
RETURNS integer
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN length(common.trim_space($1)) <= 39 AND common.trim_space($1) ~ '^[+-]?[0-9]+$' THEN
            CASE
                WHEN common.trim_space($1)::numeric BETWEEN -2147483648 AND 2147483647
                THEN common.trim_space($1)::integer
                ELSE $2
            END
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION common.try_cast(text, integer) IS
    'Try to parse text as integer (32-bit). Handles signs, whitespace, leading zeros. Returns default if invalid or out of range.';

-- 64-bit; the same rules as the integer overload.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value bigint)
RETURNS bigint
LANGUAGE sql
IMMUTABLE PARALLEL SAFE
AS $$
    SELECT CASE
        WHEN $1 IS NULL THEN $2
        WHEN length(common.trim_space($1)) <= 39 AND common.trim_space($1) ~ '^[+-]?[0-9]+$' THEN
            CASE
                WHEN common.trim_space($1)::numeric BETWEEN -9223372036854775808 AND 9223372036854775807
                THEN common.trim_space($1)::bigint
                ELSE $2
            END
        ELSE $2
    END;
$$;

COMMENT ON FUNCTION common.try_cast(text, bigint) IS
    'Try to parse text as bigint (64-bit). Handles signs, whitespace, leading zeros. Returns default if invalid or out of range.';

CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value numeric)
RETURNS numeric
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input text := common.trim_space($1);
BEGIN
    IF pg_input_is_valid(v_input, 'numeric') AND v_input !~* '(nan|inf)' THEN
        RETURN v_input::numeric;
    END IF;
    RETURN $2;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, numeric) IS
    'Try to parse text as numeric (arbitrary precision). Handles integers, decimals, scientific notation. NaN, Infinity and malformed input return the default. STABLE: pg_input_is_valid is.';

-- ISO-8601 (P1Y2M3DT4H30M), .NET TimeSpan ([-][d.]hh:mm[:ss[.fffffff]]), then
-- PostgreSQL's own parser ('2 days 3 hours', '1w 2d'). STABLE: the native
-- parser depends on DateStyle and IntervalStyle.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value interval)
RETURNS interval
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input TEXT;
    v_match TEXT[];
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;

    v_input := common.trim_space($1);
    IF v_input ~* '^[+-]?infinity$' THEN
        RETURN $2;
    END IF;
    -- Use coarse patterns to classify format, then parse accordingly
    -- This avoids expensive regex validation upfront

    -- ISO-8601: starts with optional sign + 'P', and names at least one component
    IF v_input ~* '^-?PT?$' THEN
        RETURN $2;
    END IF;
    IF v_input ~* '^-?P' THEN
        v_match := regexp_match(
            v_input,
            '^\s*(-)?P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+(?:\.\d+)?)S)?)?\s*$',
            'i'
        );
        IF v_match IS NOT NULL THEN
            -- The captures are unbounded \d+ and make_interval's params are int4,
            -- so an oversized component raises 22003 — and 7*weeks can overflow
            -- even when weeks alone fits. A "try" cast must never throw.
            BEGIN
                RETURN (CASE WHEN v_match[1] IS NULL THEN 1 ELSE -1 END) * make_interval(
                    years  => COALESCE(v_match[2]::int, 0),
                    months => COALESCE(v_match[3]::int, 0),
                    days   => COALESCE(v_match[5]::int, 0) + 7*COALESCE(v_match[4]::int, 0),
                    hours  => COALESCE(v_match[6]::int, 0),
                    mins   => COALESCE(v_match[7]::int, 0),
                    secs   => COALESCE(v_match[8]::double precision, 0)
                );
            EXCEPTION
                WHEN OTHERS THEN
                    RETURN $2;
            END;
        END IF;
    END IF;

    -- TimeSpan: contains digits with colon (hh:mm pattern)
    IF v_input ~ '\d+:\d+' THEN
        v_match := regexp_match(
            v_input,
            '^\s*(-)?(?:(\d+)\.)?(\d{1,2}):([0-5]\d)(?::([0-5]\d)(?:\.(\d{1,7}))?)?\s*$',
            'i'
        );
        IF v_match IS NOT NULL THEN
            -- Same overflow exposure as the ISO branch: the day prefix is an
            -- unbounded \d+ cast to int4.
            BEGIN
                RETURN (CASE WHEN v_match[1] IS NULL THEN 1 ELSE -1 END) * make_interval(
                    days  => COALESCE(v_match[2]::int, 0),
                    hours => COALESCE(v_match[3]::int, 0),
                    mins  => COALESCE(v_match[4]::int, 0),
                    secs  => COALESCE(v_match[5]::int, 0)
                            + COALESCE(('0.'||v_match[6])::double precision, 0)
                );
            EXCEPTION
                WHEN OTHERS THEN
                    RETURN $2;
            END;
        END IF;
    END IF;

    -- Fallback: try PostgreSQL native interval parser
    -- Handles: '2 days', '3 hours 30 minutes', '1 week 2 days', etc.
    BEGIN
        RETURN v_input::interval;
    EXCEPTION
        WHEN OTHERS THEN
            RETURN $2;
    END;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, interval) IS
    'Try to parse text as interval using ISO-8601, .NET TimeSpan, or natural language formats. Returns default value on failure.';

-- Unix epoch seconds, or milliseconds by magnitude, then PostgreSQL's own
-- parser. STABLE: text parsing depends on DateStyle, and the epoch path goes
-- through timestamptz, so the result depends on TimeZone.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value timestamp)
RETURNS timestamp
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input TEXT;
    v_numeric NUMERIC;
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;

    v_input := common.trim_space($1);
    IF v_input ~* '^[+-]?infinity$' THEN
        RETURN $2;
    END IF;

    IF v_input = '' THEN
        RETURN $2;
    END IF;

    -- Unix epoch: pure numeric (integer or decimal)
    -- Pattern: optional sign, digits, optional decimal point and digits
    IF v_input ~ '^\-?\d+(\.\d+)?$' THEN
        BEGIN
            v_numeric := v_input::numeric;

            -- Distinguish milliseconds from seconds by magnitude
            -- Millisecond epoch >= 10^10 (corresponds to year ~2286 as seconds, or ~1973 as milliseconds)
            IF abs(v_numeric) >= 10000000000 THEN
                -- Millisecond epoch: divide by 1000, same 0001..9999 bounds
                IF v_numeric BETWEEN -62135596800000 AND 253402300799999 THEN
                    RETURN to_timestamp(v_numeric / 1000.0)::timestamp;
                END IF;
            ELSE
                -- Second epoch (supports decimal for subsecond precision)
                -- Sanity check: PostgreSQL timestamp range is 4713 BC to 294276 AD
                -- Unix epoch range: -62135596800 (0001-01-01) to 253402300799 (9999-12-31)
                IF v_numeric BETWEEN -62135596800 AND 253402300799 THEN
                    RETURN to_timestamp(v_numeric)::timestamp;
                END IF;
            END IF;
        EXCEPTION
            WHEN OTHERS THEN
                -- Fall through to next strategy
        END;
    END IF;

    -- Fallback: try PostgreSQL native timestamp parser
    -- Handles: ISO-8601, common date formats, etc.
    BEGIN
        RETURN v_input::timestamp;
    EXCEPTION
        WHEN OTHERS THEN
            RETURN $2;
    END;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, timestamp) IS
    'Try to parse text as timestamp using Unix epoch (seconds/milliseconds) or standard formats. Returns default value on failure.';

-- As the timestamp overload, but an explicit ISO offset is kept. STABLE:
-- offset-less input is read in the session TimeZone.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value timestamptz)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input TEXT;
    v_numeric NUMERIC;
BEGIN
    IF $1 IS NULL THEN
        RETURN $2;
    END IF;

    v_input := common.trim_space($1);
    IF v_input ~* '^[+-]?infinity$' THEN
        RETURN $2;
    END IF;
    IF v_input = '' THEN
        RETURN $2;
    END IF;

    -- Unix epoch: pure numeric (seconds, or milliseconds by magnitude)
    IF v_input ~ '^\-?\d+(\.\d+)?$' THEN
        BEGIN
            v_numeric := v_input::numeric;
            IF abs(v_numeric) >= 10000000000 THEN
                IF v_numeric BETWEEN -62135596800000 AND 253402300799999 THEN
                    RETURN to_timestamp(v_numeric / 1000.0);
                END IF;
            ELSIF v_numeric BETWEEN -62135596800 AND 253402300799 THEN
                RETURN to_timestamp(v_numeric);
            END IF;
        EXCEPTION
            WHEN OTHERS THEN
                -- Fall through to native parser
        END;
    END IF;

    BEGIN
        RETURN v_input::timestamptz;
    EXCEPTION
        WHEN OTHERS THEN
            RETURN $2;
    END;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, timestamptz) IS
    'Try to parse text as timestamptz using Unix epoch (seconds/milliseconds) or standard formats; preserves explicit ISO offsets. Returns default value on failure.';

-- pg_input_is_valid checks without a subtransaction. PL/pgSQL, not SQL: an
-- inlined CASE would let the planner fold the cast of a constant and raise.
-- STABLE: date input depends on DateStyle, and pg_input_is_valid is STABLE.
CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value date)
RETURNS date
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input text := common.trim_space($1);
BEGIN
    IF pg_input_is_valid(v_input, 'date') AND v_input !~* '^[+-]?infinity$' THEN
        RETURN v_input::date;
    END IF;
    RETURN $2;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, date) IS
    'Try to parse text as date. Infinity and malformed input return the default.';

CREATE OR REPLACE FUNCTION common.try_cast(input text, default_value inet)
RETURNS inet
LANGUAGE plpgsql
STABLE PARALLEL SAFE
AS $$
DECLARE
    v_input text := common.trim_space($1);
BEGIN
    IF pg_input_is_valid(v_input, 'inet') THEN
        RETURN v_input::inet;
    END IF;
    RETURN $2;
END;
$$;

COMMENT ON FUNCTION common.try_cast(text, inet) IS
    'Try to parse text as inet (IPv4 or IPv6 address, optional prefix). Malformed input returns the default.';

-- Every overload: parsed values, boundaries, and the default for malformed,
-- non-finite, overflowing or NULL input. Each case compares the result and
-- the expectation as text of the same type.
DO $$
DECLARE
    v_case record;
BEGIN
    FOR v_case IN
        SELECT * FROM (VALUES
            ('trim keeps letters', common.trim_space(E'\tv1v\x0b'), 'v1v'),
            ('uuid standard', common.try_cast('550e8400-e29b-41d4-a716-446655440000', extensions.uuid_nil())::text, '550e8400-e29b-41d4-a716-446655440000'),
            ('uuid compact', common.try_cast('550e8400e29b41d4a716446655440000', extensions.uuid_nil())::text, '550e8400-e29b-41d4-a716-446655440000'),
            ('uuid tab/newline', common.try_cast(E'\t550e8400-e29b-41d4-a716-446655440000\n', NULL::uuid)::text, '550e8400-e29b-41d4-a716-446655440000'),
            ('uuid invalid', common.try_cast('invalid', extensions.uuid_nil())::text, extensions.uuid_nil()::text),
            ('uuid NULL', common.try_cast(NULL::text, extensions.uuid_nil())::text, extensions.uuid_nil()::text),
            ('uuid_is_nil nil', common.uuid_is_nil(extensions.uuid_nil())::text, 'true'),
            ('uuid_is_nil other', common.uuid_is_nil('550e8400-e29b-41d4-a716-446655440000'::uuid)::text, 'false'),
            ('boolean true', common.try_cast('true', false)::text, 'true'),
            ('boolean TRUE', common.try_cast('TRUE', false)::text, 'true'),
            ('boolean t', common.try_cast('t', false)::text, 'true'),
            ('boolean yes', common.try_cast('yes', false)::text, 'true'),
            ('boolean Y', common.try_cast('Y', false)::text, 'true'),
            ('boolean on', common.try_cast('on', false)::text, 'true'),
            ('boolean 1', common.try_cast('1', false)::text, 'true'),
            ('boolean spaced 1', common.try_cast('  1  ', false)::text, 'true'),
            ('boolean tab', common.try_cast(E'\tyes', NULL::boolean)::text, 'true'),
            ('boolean false', common.try_cast('false', true)::text, 'false'),
            ('boolean FALSE', common.try_cast('FALSE', true)::text, 'false'),
            ('boolean f', common.try_cast('f', true)::text, 'false'),
            ('boolean no', common.try_cast('no', true)::text, 'false'),
            ('boolean N', common.try_cast('N', true)::text, 'false'),
            ('boolean off', common.try_cast('off', true)::text, 'false'),
            ('boolean 0', common.try_cast('0', true)::text, 'false'),
            ('boolean spaced 0', common.try_cast('  0  ', true)::text, 'false'),
            ('boolean invalid, default false', common.try_cast('invalid', false)::text, 'false'),
            ('boolean invalid, default true', common.try_cast('invalid', true)::text, 'true'),
            ('boolean 2', common.try_cast('2', false)::text, 'false'),
            ('boolean empty', common.try_cast('', false)::text, 'false'),
            ('boolean NULL', common.try_cast(NULL::text, true)::text, 'true'),
            ('integer 42', common.try_cast('42', 0)::text, '42'),
            ('integer -123', common.try_cast('-123', 0)::text, '-123'),
            ('integer +99', common.try_cast('+99', 0)::text, '99'),
            ('integer 0', common.try_cast('0', -1)::text, '0'),
            ('integer leading zeros', common.try_cast('007', 0)::text, '7'),
            ('integer spaced', common.try_cast('  42  ', 0)::text, '42'),
            ('integer newline', common.try_cast(E'42\r\n', NULL::integer)::text, '42'),
            ('integer max', common.try_cast('2147483647', 0)::text, '2147483647'),
            ('integer min', common.try_cast('-2147483648', 0)::text, '-2147483648'),
            ('integer overflow', common.try_cast('2147483648', 0)::text, '0'),
            ('integer underflow', common.try_cast('-2147483649', 0)::text, '0'),
            ('integer large overflow', common.try_cast('9999999999', 0)::text, '0'),
            ('integer overflow to NULL', common.try_cast('2147483648', NULL::integer)::text, NULL),
            ('integer 131k digits', common.try_cast(repeat('9', 131073), 0)::text, '0'),
            ('integer decimal', common.try_cast('12.5', 0)::text, '0'),
            ('integer scientific', common.try_cast('1e5', 0)::text, '0'),
            ('integer invalid', common.try_cast('invalid', 0)::text, '0'),
            ('integer empty', common.try_cast('', 0)::text, '0'),
            ('integer mixed', common.try_cast('12abc', 0)::text, '0'),
            ('integer NULL', common.try_cast(NULL::text, 42)::text, '42'),
            ('bigint 42', common.try_cast('42', 0::bigint)::text, '42'),
            ('bigint -123', common.try_cast('-123', 0::bigint)::text, '-123'),
            ('bigint unix ms', common.try_cast('1705327800000', 0::bigint)::text, '1705327800000'),
            ('bigint spaced', common.try_cast('  999999999999  ', 0::bigint)::text, '999999999999'),
            ('bigint tab', common.try_cast(E'\t7', NULL::bigint)::text, '7'),
            ('bigint max', common.try_cast('9223372036854775807', 0::bigint)::text, '9223372036854775807'),
            ('bigint min', common.try_cast('-9223372036854775808', 0::bigint)::text, '-9223372036854775808'),
            ('bigint overflow', common.try_cast('9223372036854775808', 0::bigint)::text, '0'),
            ('bigint underflow', common.try_cast('-9223372036854775809', 0::bigint)::text, '0'),
            ('bigint large overflow', common.try_cast('99999999999999999999', 0::bigint)::text, '0'),
            ('bigint 131k digits', common.try_cast(repeat('9', 131073), 0::bigint)::text, '0'),
            ('bigint decimal', common.try_cast('12.5', 0::bigint)::text, '0'),
            ('bigint scientific', common.try_cast('1e10', 0::bigint)::text, '0'),
            ('bigint invalid', common.try_cast('invalid', 0::bigint)::text, '0'),
            ('bigint NULL', common.try_cast(NULL::text, 42::bigint)::text, '42'),
            ('numeric 42', common.try_cast('42', 0::numeric)::text, '42'),
            ('numeric -123', common.try_cast('-123', 0::numeric)::text, '-123'),
            ('numeric +99', common.try_cast('+99', 0::numeric)::text, '99'),
            ('numeric 19.99', common.try_cast('19.99', 0::numeric)::text, '19.99'),
            ('numeric 0.0001', common.try_cast('0.0001', 0::numeric)::text, '0.0001'),
            ('numeric leading dot', common.try_cast('.5', 0::numeric)::text, '0.5'),
            ('numeric trailing dot', common.try_cast('123.', 0::numeric)::text, '123'),
            ('numeric spaced', common.try_cast('  3.14  ', 0::numeric)::text, '3.14'),
            ('numeric newline', common.try_cast(E'\n3.5\t', NULL::numeric)::text, '3.5'),
            ('numeric 1.5e10', common.try_cast('1.5e10', 0::numeric)::text, '15000000000'),
            ('numeric 2.5E-3', common.try_cast('2.5E-3', 0::numeric)::text, '0.0025'),
            ('numeric 1e+6', common.try_cast('1e+6', 0::numeric)::text, '1000000'),
            ('numeric 5E2', common.try_cast('5E2', 0::numeric)::text, '500'),
            ('numeric precision', common.try_cast('123456789.123456789', 0::numeric)::text, '123456789.123456789'),
            ('numeric invalid', common.try_cast('invalid', 0::numeric)::text, '0'),
            ('numeric empty', common.try_cast('', 0::numeric)::text, '0'),
            ('numeric two dots', common.try_cast('12.34.56', 0::numeric)::text, '0'),
            ('numeric mixed', common.try_cast('12abc', 0::numeric)::text, '0'),
            ('numeric NaN', common.try_cast('NaN', NULL::numeric)::text, NULL),
            ('numeric -Infinity', common.try_cast('-Infinity', NULL::numeric)::text, NULL),
            ('numeric inf', common.try_cast('inf', NULL::numeric)::text, NULL),
            ('numeric overflow', common.try_cast('1e1000000', NULL::numeric)::text, NULL),
            ('numeric NULL', common.try_cast(NULL::text, 1.5::numeric)::text, '1.5'),
            ('interval ISO P1Y2M3D', common.try_cast('P1Y2M3D', '0'::interval)::text, make_interval(years => 1, months => 2, days => 3)::text),
            ('interval ISO PT5H30M', common.try_cast('PT5H30M', '0'::interval)::text, make_interval(hours => 5, mins => 30)::text),
            ('interval ISO P2W', common.try_cast('P2W', '0'::interval)::text, make_interval(days => 14)::text),
            ('interval ISO fraction', common.try_cast('PT1.5S', NULL::interval)::text, interval '1.5 seconds'::text),
            ('interval TimeSpan 12:30', common.try_cast('12:30', '0'::interval)::text, make_interval(hours => 12, mins => 30)::text),
            ('interval TimeSpan 1.12:30:45', common.try_cast('1.12:30:45', '0'::interval)::text, make_interval(days => 1, hours => 12, mins => 30, secs => 45)::text),
            ('interval 2 days', common.try_cast('2 days', '0'::interval)::text, make_interval(days => 2)::text),
            ('interval 3 hours 30 minutes', common.try_cast('3 hours 30 minutes', '0'::interval)::text, make_interval(hours => 3, mins => 30)::text),
            ('interval 1w 2d 3h', common.try_cast('1w 2d 3h', '0'::interval)::text, make_interval(days => 9, hours => 3)::text),
            ('interval 90 sec', common.try_cast('90 sec', '0'::interval)::text, make_interval(secs => 90)::text),
            ('interval invalid', common.try_cast('invalid', '0'::interval)::text, '0'::interval::text),
            ('interval empty', common.try_cast('', '0'::interval)::text, '0'::interval::text),
            ('interval infinity', common.try_cast('infinity', NULL::interval)::text, NULL),
            ('interval bare P', common.try_cast('P', NULL::interval)::text, NULL),
            ('interval bare PT', common.try_cast(' PT ', NULL::interval)::text, NULL),
            ('interval oversized ISO year', common.try_cast('P99999999999Y', NULL::interval)::text, NULL),
            ('interval oversized ISO day', common.try_cast('P99999999999D', NULL::interval)::text, NULL),
            ('interval oversized TimeSpan day', common.try_cast('99999999999999.12:30:45', NULL::interval)::text, NULL),
            ('interval 7*weeks overflow', common.try_cast('P500000000W', NULL::interval)::text, NULL),
            ('interval NULL', common.try_cast(NULL::text, interval '1 day')::text, interval '1 day'::text),
            ('timestamp ISO', common.try_cast('2025-01-15 14:30:00', NULL::timestamp)::text, timestamp '2025-01-15 14:30:00'::text),
            ('timestamp ISO T', common.try_cast('2025-01-15T14:30:00', NULL::timestamp)::text, timestamp '2025-01-15 14:30:00'::text),
            ('timestamp date only', common.try_cast('2025-01-15', NULL::timestamp)::text, timestamp '2025-01-15'::text),
            ('timestamp epoch s', common.try_cast('1705327800', NULL::timestamp)::text, to_timestamp(1705327800)::timestamp::text),
            ('timestamp epoch s decimal', common.try_cast('1705327800.500', NULL::timestamp)::text, to_timestamp(1705327800.500)::timestamp::text),
            ('timestamp epoch ms', common.try_cast('1705327800000', NULL::timestamp)::text, to_timestamp(1705327800)::timestamp::text),
            ('timestamp negative epoch', common.try_cast('-86400', NULL::timestamp)::text, to_timestamp(-86400)::timestamp::text),
            ('timestamp invalid', common.try_cast('invalid timestamp', timestamp '2025-01-01')::text, timestamp '2025-01-01'::text),
            ('timestamp empty', common.try_cast('', timestamp '2025-01-01')::text, timestamp '2025-01-01'::text),
            ('timestamp month 13', common.try_cast('2025-13-01', timestamp '2025-01-01')::text, timestamp '2025-01-01'::text),
            ('timestamp infinity', common.try_cast('infinity', NULL::timestamp)::text, NULL),
            ('timestamp ms epoch out of range', common.try_cast('300000000000000', NULL::timestamp)::text, NULL),
            ('timestamp NULL', common.try_cast(NULL::text, timestamp '2025-01-01')::text, timestamp '2025-01-01'::text),
            ('timestamptz offset kept', common.try_cast('2025-01-15T14:30:00+02:00', NULL::timestamptz)::text, '2025-01-15T12:30:00+00'::timestamptz::text),
            ('timestamptz epoch s', common.try_cast('1705327800', NULL::timestamptz)::text, to_timestamp(1705327800)::text),
            ('timestamptz epoch ms', common.try_cast('1705327800000', NULL::timestamptz)::text, to_timestamp(1705327800)::text),
            ('timestamptz invalid', common.try_cast('not a timestamp', timestamptz '2025-01-01 00:00:00+00')::text, timestamptz '2025-01-01 00:00:00+00'::text),
            ('timestamptz empty', common.try_cast('', timestamptz '2025-01-01 00:00:00+00')::text, timestamptz '2025-01-01 00:00:00+00'::text),
            ('timestamptz -infinity', common.try_cast(E'\t-infinity', NULL::timestamptz)::text, NULL),
            ('timestamptz ms epoch out of range', common.try_cast('-100000000000000', NULL::timestamptz)::text, NULL),
            ('timestamptz NULL', common.try_cast(NULL::text, timestamptz '2025-01-01 00:00:00+00')::text, timestamptz '2025-01-01 00:00:00+00'::text),
            ('date', common.try_cast(E' 2025-01-15\n', NULL::date)::text, date '2025-01-15'::text),
            ('date invalid', common.try_cast('2025-02-30', NULL::date)::text, NULL),
            ('date infinity', common.try_cast('infinity', NULL::date)::text, NULL),
            ('date NULL', common.try_cast(NULL, date '2000-01-01')::text, date '2000-01-01'::text),
            ('inet v4', common.try_cast(' 10.0.0.1 ', NULL::inet)::text, '10.0.0.1/32'),
            ('inet v6 prefix', common.try_cast('2001:db8::/32', NULL::inet)::text, '2001:db8::/32'),
            ('inet invalid', common.try_cast('10.0.0.256', NULL::inet)::text, NULL),
            ('inet NULL', common.try_cast(NULL, inet '127.0.0.1')::text, '127.0.0.1/32')
        ) AS c(name, got, want)
    LOOP
        IF v_case.got IS DISTINCT FROM v_case.want THEN
            RAISE EXCEPTION 'try_cast failed: % gave %, want %', v_case.name, v_case.got, v_case.want;
        END IF;
    END LOOP;
END $$;
